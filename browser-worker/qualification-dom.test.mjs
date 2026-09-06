import assert from 'node:assert/strict';
import test from 'node:test';
import { chromium } from 'playwright-core';

import {
  applyOptionSelection,
  captureChoiceQuestion,
  questionDOMSignature,
} from './qualification-dom.mjs';

const executablePath = process.env.QUALIFICATION_TEST_BROWSER ||
  '/etc/profiles/per-user/darkon/bin/google-chrome-stable';

async function withPage(html, check) {
  const browser = await chromium.launch({ executablePath, headless: true });
  try {
    const page = await browser.newPage();
    await page.setContent(html);
    await check(page);
  } finally {
    await browser.close();
  }
}

test('captures only working radios and selects by runtime id', async () => {
  await withPage(`
    <main>
      <p>Which command lists containers?</p>
      <section>
        <label><input type="radio" name="answer" value="uuid-a"><span>docker ps</span>
          <input type="radio" aria-hidden="true"></label>
        <label><input type="radio" name="answer" value="uuid-b"><span>docker rm</span>
          <input type="radio" aria-hidden="true"></label>
      </section>
      <button data-qa="footer-next-button" disabled>Next</button>
    </main>
    <script>
      for (const input of document.querySelectorAll('input[name="answer"]')) {
        input.addEventListener('change', () => {
          document.querySelector('[data-qa="footer-next-button"]').disabled = false;
        });
      }
    </script>`, async page => {
    const before = await captureChoiceQuestion(page);
    assert.equal(before.text, 'Which command lists containers?');
    assert.equal(before.kind, 'single');
    assert.deepEqual(before.options, [
      { id: 'uuid-a', text: 'docker ps' },
      { id: 'uuid-b', text: 'docker rm' },
    ]);
    await applyOptionSelection(page, ['uuid-b']);
    const after = await captureChoiceQuestion(page);
    assert.deepEqual(after.selected_option_ids, ['uuid-b']);
    assert.equal(await page.locator('[data-qa="footer-next-button"]').isEnabled(), true);
  });
});

test('multiple choice retry converges to the exact requested set', async () => {
  await withPage(`
    <main>
      <p>Select two properties.</p>
      <label><input type="checkbox" name="answer" value="a" checked>A</label>
      <label><input type="checkbox" name="answer" value="b" checked>B</label>
      <label><input type="checkbox" name="answer" value="c">C</label>
    </main>`, async page => {
    await applyOptionSelection(page, ['a', 'c']);
    assert.deepEqual((await captureChoiceQuestion(page)).selected_option_ids, ['a', 'c']);
    await applyOptionSelection(page, ['b']);
    assert.deepEqual((await captureChoiceQuestion(page)).selected_option_ids, ['b']);
  });
});

test('rejects duplicate runtime ids instead of selecting ambiguously', async () => {
  await withPage(`
    <main>
      <p>Choose.</p>
      <label><input type="radio" name="answer" value="same">A</label>
      <label><input type="radio" name="answer" value="same">B</label>
    </main>`, async page => {
    await assert.rejects(() => captureChoiceQuestion(page), /duplicate runtime option id/);
  });
});

test('question signature ignores presentation order but detects changed text or options', () => {
  const original = {
    text: 'Choose.', kind: 'single',
    options: [{ id: 'a', text: 'Alpha' }, { id: 'b', text: 'Beta' }],
  };
  const reordered = {
    text: ' Choose. ', kind: 'single',
    options: [{ id: 'b', text: 'Beta' }, { id: 'a', text: 'Alpha' }],
  };
  const changedOption = {
    ...original,
    options: [{ id: 'a', text: 'Alpha' }, { id: 'b', text: 'Gamma' }],
  };
  assert.equal(questionDOMSignature(original), questionDOMSignature(reordered));
  assert.notEqual(questionDOMSignature(original), questionDOMSignature(changedOption));
  assert.notEqual(questionDOMSignature(original), questionDOMSignature({ ...original, text: 'Choose two.' }));
});
