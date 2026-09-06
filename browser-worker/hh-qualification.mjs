import fs from 'node:fs';
import readline from 'node:readline';
import { chromium } from 'playwright-core';

const options = parseArguments(process.argv.slice(2));
const browser = await chromium.launch({
  executablePath: options.browser,
  headless: options.headless,
  proxy: options.proxy ? { server: options.proxy } : undefined,
});
const context = await browser.newContext({
  storageState: options.stateFile,
  locale: 'ru-RU',
  viewport: { width: 1440, height: 960 },
});
let page;
let selectedKind = '';

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
for await (const line of input) {
  if (!line.trim()) continue;
  let command;
  try {
    command = JSON.parse(line);
    const result = await dispatch(command);
    send({ id: command.id, ok: true, ...result });
  } catch (error) {
    send({ id: command?.id ?? 0, ok: false, error: publicError(error) });
  }
}
await browser.close();

async function dispatch(command) {
  switch (command.type) {
    case 'open':
      return { offering: await openOffering(command) };
    case 'start':
      return { capture: await startAttempt() };
    case 'capture':
      return { capture: await captureQuestion() };
    case 'select':
      return { capture: await selectOptions(command) };
    case 'next':
      return { capture: await submitAndContinue(command) };
    case 'close':
      await browser.close();
      process.exit(0);
    default:
      throw new Error(`unsupported command ${JSON.stringify(command.type)}`);
  }
}

async function openOffering(command) {
  if (!/^\d+$/.test(command.skill_id ?? '')) throw new Error('skill_id must be numeric');
  if (!command.level) throw new Error('level is required');
  if (!['theory', 'practice'].includes(command.kind)) throw new Error('kind must be theory or practice');

  page = await context.newPage();
  const url = `https://hh.ru/applicant/skills/${command.skill_id}/verification_methods`;
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await ensureAuthenticated(page);

  const levelTab = page.getByRole('tab', { name: command.level, exact: true });
  try {
    await levelTab.waitFor({ state: 'visible', timeout: 10_000 });
  } catch {
    throw new Error(`qualification level not found: ${command.level}`);
  }
  await levelTab.click();

  const kindCard = page.locator(
    `[data-qa="applicant-keyskills-verification-methods-kind-card-${command.kind}"]`,
  );
  if (await kindCard.count() > 0) await kindCard.first().locator('xpath=..').click();
  selectedKind = command.kind;

  const start = startButton(command.kind);
  await start.waitFor({ state: 'visible', timeout: 10_000 });
  const summary = await start.locator('xpath=..').innerText().catch(() => '');
  return {
    skill_id: command.skill_id,
    family_name: normalize(await page.locator('h1').first().innerText().catch(() => page.title())),
    level: command.level,
    kind: command.kind,
    url: page.url(),
    summary: normalize(summary),
    start_available: await start.isEnabled(),
  };
}

async function startAttempt() {
  requirePage();
  if (!selectedKind) throw new Error('qualification kind is not selected');
  const start = startButton(selectedKind);
  if (!(await start.isEnabled())) throw new Error('qualification start button is disabled');

  await start.click();
  page = await waitForAssessmentPage(15_000);
  if (!page.url().startsWith('https://assessment.hh.ru/')) {
    throw new Error(`assessment page did not open; current URL is ${page.url()}`);
  }
  await page.waitForLoadState('domcontentloaded');
  return captureQuestion();
}

async function captureQuestion() {
  requireAssessmentPage();
  const inputs = page.locator('input[name="answer"]');
  try {
    await inputs.first().waitFor({ state: 'attached', timeout: 8_000 });
  } catch {
    const text = normalize(await page.locator('body').innerText().catch(() => ''));
    if (/результат|завершен|завершён|подтвержден|подтверждён|правильн/i.test(text)) {
      return { status: 'completed', url: page.url(), title: await page.title() };
    }
    throw new Error('assessment page has no supported answer controls');
  }

  const options = [];
  let kind = 'single';
  for (let index = 0; index < await inputs.count(); index += 1) {
    const input = inputs.nth(index);
    const observed = await input.evaluate((element, optionIndex) => {
      const label = element.closest('label');
      const visibleText = label?.innerText || element.getAttribute('aria-label') || '';
      return {
        id: element.value || `option-${optionIndex + 1}`,
        text: visibleText.replace(/\s+/g, ' ').trim(),
        checked: element.checked,
        input_type: element.type,
      };
    }, index);
    if (!observed.text) throw new Error(`answer option ${index + 1} has no visible text`);
    if (observed.input_type === 'checkbox') kind = 'multiple';
    options.push(observed);
  }

  const questionText = await inputs.first().evaluate(element => {
    const top = (element.closest('label') ?? element).getBoundingClientRect().top;
    const root = document.querySelector('main, [role="main"]') ?? document;
    const candidates = Array.from(root.querySelectorAll('p, pre'))
      .filter(candidate => !candidate.closest('label, button, header, nav'))
      .map(candidate => {
        const box = candidate.getBoundingClientRect();
        return {
          text: candidate.innerText?.replace(/\s+/g, ' ').trim(),
          bottom: box.bottom,
          visible: box.width > 0 && box.height > 0,
        };
      })
      .filter(candidate => candidate.visible && candidate.text && candidate.bottom <= top + 2)
      .sort((left, right) => right.bottom - left.bottom);
    return candidates[0]?.text ?? '';
  });
  if (!questionText) throw new Error('assessment question text was not found');

  const progress = await readProgress();
  const timeLeftSeconds = await readTimeLeft();
  return {
    status: 'question',
    url: page.url(),
    title: await page.title(),
    question: {
      id: `runtime-question-${progress.current || 0}`,
      text: normalize(questionText),
      kind,
      options: options.map(option => ({ id: option.id, text: option.text })),
    },
    selected_option_ids: options.filter(option => option.checked).map(option => option.id),
    progress,
    time_left_seconds: timeLeftSeconds,
  };
}

async function selectOptions(command) {
  const capture = await captureQuestion();
  assertCurrentQuestion(capture, command);
  const requested = new Set(command.option_ids ?? []);
  if (requested.size === 0) throw new Error('at least one option id is required');
  if (capture.question.kind === 'single' && requested.size !== 1) {
    throw new Error('single-choice question requires exactly one option');
  }
  const known = new Set(capture.question.options.map(option => option.id));
  for (const id of requested) {
    if (!known.has(id)) throw new Error(`unknown runtime option id ${id}`);
  }

  const inputs = page.locator('input[name="answer"]');
  for (let index = 0; index < await inputs.count(); index += 1) {
    const input = inputs.nth(index);
    const id = await input.inputValue();
    const checked = await input.isChecked();
    const wanted = requested.has(id);
    if (checked !== wanted) await input.click();
  }
  return captureQuestion();
}

async function submitAndContinue(command) {
  const before = await captureQuestion();
  assertCurrentQuestion(before, command);
  const selected = [...before.selected_option_ids].sort();
  const expected = [...(command.option_ids ?? [])].sort();
  if (JSON.stringify(selected) !== JSON.stringify(expected)) {
    throw new Error('checked options do not match the confirmed selection');
  }

  const button = page.locator('button[data-qa="footer-next-button"]');
  await button.waitFor({ state: 'visible', timeout: 5_000 });
  if (!(await button.isEnabled())) throw new Error('next button is disabled');
  await button.click();
  await page.waitForTimeout(250);
  await page.waitForFunction(
    previousIDs => {
      const current = Array.from(document.querySelectorAll('input[name="answer"]'))
        .map(input => input.value)
        .sort();
      return current.length === 0 || JSON.stringify(current) !== JSON.stringify(previousIDs);
    },
    before.question.options.map(option => option.id).sort(),
    { timeout: 15_000 },
  ).catch(() => {});
  return captureQuestion();
}

function assertCurrentQuestion(capture, command) {
  if (capture.status !== 'question') throw new Error('assessment no longer shows a question');
  if (capture.question.text !== command.expected_question) {
    throw new Error('assessment question changed before the command was applied');
  }
}

async function readProgress() {
  const body = await page.locator('body').innerText();
  const match = body.match(/(\d+)\s+из\s+(\d+)/i);
  if (match) return { current: Number(match[1]), total: Number(match[2]) };

  const progress = page.locator('[role="progressbar"][data-qa="progress"]');
  const current = Number(await progress.getAttribute('aria-valuenow')) || 0;
  const total = Number(await progress.getAttribute('aria-valuemax')) || 0;
  return { current, total };
}

async function readTimeLeft() {
  try {
    return await page.evaluate(async () => {
      const response = await fetch('/shards/contest/get_time_left', { credentials: 'include' });
      if (!response.ok) return 0;
      const payload = await response.json();
      return Number(payload.timeLeftSeconds) || 0;
    });
  } catch {
    return 0;
  }
}

function startButton(kind) {
  const byQA = page.locator(`[data-qa="applicant-keyskills-verification-methods-start-${kind}"]`);
  return byQA.or(page.getByRole('button', { name: /Начать тест/i })).first();
}

async function waitForAssessmentPage(timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (page?.url().startsWith('https://assessment.hh.ru/')) return page;
    const candidate = context.pages().find(item => item.url().startsWith('https://assessment.hh.ru/'));
    if (candidate) return candidate;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('assessment page timeout');
}

async function ensureAuthenticated(candidate) {
  if (/\/account\/login|\/login/.test(new URL(candidate.url()).pathname)) {
    throw new Error('HH browser state is not authenticated');
  }
}

function requirePage() {
  if (!page) throw new Error('offering is not open');
}

function requireAssessmentPage() {
  requirePage();
  if (!page.url().startsWith('https://assessment.hh.ru/')) {
    throw new Error(`current page is not an assessment: ${page.url()}`);
  }
}

function normalize(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}

function publicError(error) {
  return error instanceof Error ? error.message : String(error);
}

function send(value) {
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

function parseArguments(args) {
  const result = {
    browser: '/etc/profiles/per-user/darkon/bin/google-chrome-stable',
    headless: false,
    proxy: '',
    stateFile: '',
  };
  for (let index = 0; index < args.length; index += 1) {
    switch (args[index]) {
      case '--browser':
        result.browser = args[++index];
        break;
      case '--state-file':
        result.stateFile = args[++index];
        break;
      case '--proxy':
        result.proxy = args[++index];
        break;
      case '--headless':
        result.headless = true;
        break;
      default:
        throw new Error(`unknown argument ${args[index]}`);
    }
  }
  if (!result.stateFile || !fs.statSync(result.stateFile).isFile()) {
    throw new Error('valid --state-file is required');
  }
  return result;
}
