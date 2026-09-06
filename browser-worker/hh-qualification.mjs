import fs from 'node:fs';
import readline from 'node:readline';
import { chromium } from 'playwright-core';

import {
  applyOptionSelection,
  captureChoiceQuestion,
  questionDOMSignature,
  sameIDs,
} from './qualification-dom.mjs';

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
await context.route('**/*', route => {
  if (['font', 'image', 'media'].includes(route.request().resourceType())) return route.abort();
  return route.continue();
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
  const url = `https://hh.ru/applicant/skills/${command.skill_id}/verification_methods?kind=${command.kind}`;
  progress('загружаю страницу HH');
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await ensureAuthenticated(page);
  progress('страница HH загружена, ожидаю список уровней');

  const levelTab = page.getByRole('tab', { name: command.level, exact: true });
  try {
    await levelTab.waitFor({ state: 'visible', timeout: 10_000 });
  } catch {
    throw new Error(`qualification level not found: ${command.level}`);
  }
  if (await levelTab.getAttribute('aria-selected') !== 'true') {
    await levelTab.click({ timeout: 10_000 });
  }
  progress(`уровень «${command.level}» выбран`);

  const kindCard = page.locator(
    `[data-qa="applicant-keyskills-verification-methods-kind-card-${command.kind}"]`,
  );
  if (new URL(page.url()).searchParams.get('kind') !== command.kind && await kindCard.count() > 0) {
    await kindCard.first().locator('xpath=..').click({ timeout: 10_000 });
  }
  selectedKind = command.kind;
  progress(`тип «${command.kind}» выбран`);

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

  progress('открываю обязательные инструкции перед стартом');
  await start.click({ timeout: 10_000 });
  const surface = await waitForStartSurface(5_000);
  if (surface === 'modal') {
    let actualStart;
    for (let step = 0; step < 6; step += 1) {
      actualStart = page.locator('[data-qa="modal-start-btn"]');
      if (await actualStart.isVisible()) break;
      const next = page.locator('[data-qa="modal-next-btn"]');
      if (!(await next.isVisible())) throw new Error('pre-start modal has no next or start control');
      progress(`информационный экран ${step + 1} прочитан`);
      await next.click({ timeout: 5_000 });
      await page.waitForTimeout(100);
    }
    if (!actualStart || !(await actualStart.isVisible())) {
      throw new Error('pre-start modal exceeded the supported number of steps');
    }
    progress('подтверждаю финальный старт и запускаю серверный таймер');
    await actualStart.click({ timeout: 10_000 });
  }
  progress('ожидаю страницу assessment.hh.ru');
  page = await waitForAssessmentPage(30_000);
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

  const observed = await captureChoiceQuestion(page);

  const progress = await readProgress();
  const timeLeftSeconds = await readTimeLeft();
  return {
    status: 'question',
    url: page.url(),
    title: await page.title(),
    question: {
      id: `runtime-question-${progress.current || 0}`,
      text: observed.text,
      kind: observed.kind,
      options: observed.options,
    },
    selected_option_ids: observed.selected_option_ids,
    progress,
    time_left_seconds: timeLeftSeconds,
  };
}

async function selectOptions(command) {
  const capture = await captureQuestion();
  assertCurrentQuestion(capture, command);
  await applyOptionSelection(page, command.option_ids);
  const marked = await captureQuestion();
  if (!sameIDs(marked.selected_option_ids, command.option_ids ?? [])) {
    throw new Error('assessment selection verification failed');
  }
  return marked;
}

async function submitAndContinue(command) {
  const before = await captureQuestion();
  assertCurrentQuestion(before, command);
  if (!sameIDs(before.selected_option_ids, command.option_ids ?? [])) {
    throw new Error('checked options do not match the confirmed selection');
  }

  const button = page.locator('button[data-qa="footer-next-button"]');
  await button.waitFor({ state: 'visible', timeout: 5_000 });
  if (!(await button.isEnabled())) throw new Error('next button is disabled');
  await button.click();
  return waitForNextCapture(before, 20_000);
}

async function waitForNextCapture(before, timeout) {
  const previousSignature = questionDOMSignature(before.question);
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const inputs = page.locator('input[name="answer"]');
      if (await inputs.count() > 0) {
        const current = await captureChoiceQuestion(page);
        if (questionDOMSignature(current) !== previousSignature) return captureQuestion();
      } else {
        const text = normalize(await page.locator('body').innerText().catch(() => ''));
        if (/результат|завершен|завершён|подтвержден|подтверждён|правильн/i.test(text)) {
          return { status: 'completed', url: page.url(), title: await page.title() };
        }
      }
    } catch (error) {
      lastError = error;
    }
    await page.waitForTimeout(250);
  }
  const suffix = lastError ? `; last DOM error: ${publicError(lastError)}` : '';
  throw new Error(`HH did not show a different question after Next; the button was not clicked twice${suffix}`);
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
  throw new Error(`assessment page timeout; open pages: ${publicPageLocations()}`);
}

async function waitForStartSurface(timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const assessment = context.pages().find(item => item.url().startsWith('https://assessment.hh.ru/'));
    if (assessment || page?.url().startsWith('https://assessment.hh.ru/')) return 'assessment';
    if (await page.locator('[data-qa="modal-next-btn"], [data-qa="modal-start-btn"]').first().isVisible()) {
      return 'modal';
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`start click opened neither instructions nor assessment; open pages: ${publicPageLocations()}`);
}

function publicPageLocations() {
  return context.pages().map(candidate => {
    try {
      const url = new URL(candidate.url());
      const path = url.pathname.replace(/\/tests\/[^/]+/, '/tests/<attempt>');
      return `${url.origin}${path}`;
    } catch {
      return '<invalid-url>';
    }
  }).join(', ');
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

function progress(message) {
  process.stderr.write(`[browser] ${message}\n`);
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
