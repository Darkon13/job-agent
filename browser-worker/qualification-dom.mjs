export async function captureChoiceQuestion(page) {
  const inputs = page.locator('input[name="answer"]');
  const count = await inputs.count();
  if (count === 0) throw new Error('assessment page has no answer controls');

  const options = [];
  const runtimeIDs = new Set();
  let inputType = '';
  for (let index = 0; index < count; index += 1) {
    const input = inputs.nth(index);
    const observed = await input.evaluate(element => {
      const label = element.closest('label') ||
        (element.id ? document.querySelector(`label[for="${CSS.escape(element.id)}"]`) : null);
      const textSource = label || element.parentElement;
      return {
        id: element.value,
        text: textSource?.innerText || element.getAttribute('aria-label') || '',
        checked: element.checked,
        input_type: element.type,
      };
    });
    observed.id = normalize(observed.id);
    observed.text = normalize(observed.text);
    if (!observed.id) throw new Error(`answer option ${index + 1} has no runtime id`);
    if (!observed.text) throw new Error(`answer option ${index + 1} has no visible text`);
    if (!['radio', 'checkbox'].includes(observed.input_type)) {
      throw new Error(`answer option ${index + 1} has unsupported input type ${observed.input_type}`);
    }
    if (inputType && inputType !== observed.input_type) {
      throw new Error('assessment mixes radio and checkbox answer controls');
    }
    if (runtimeIDs.has(observed.id)) throw new Error(`duplicate runtime option id ${observed.id}`);
    inputType = observed.input_type;
    runtimeIDs.add(observed.id);
    options.push(observed);
  }

  const questionText = await inputs.first().evaluate(element => {
    const top = (element.closest('label') ?? element).getBoundingClientRect().top;
    const root = document.querySelector('main, [role="main"]') ?? document;
    const explicit = root.querySelector('[data-qa="question-text"], [data-qa="task-question"]');
    const explicitText = explicit?.innerText?.replace(/\s+/g, ' ').trim();
    if (explicitText) return explicitText;
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
  if (!normalize(questionText)) throw new Error('assessment question text was not found');

  return {
    text: normalize(questionText),
    kind: inputType === 'checkbox' ? 'multiple' : 'single',
    options: options.map(option => ({ id: option.id, text: option.text })),
    selected_option_ids: options.filter(option => option.checked).map(option => option.id),
  };
}

// applyOptionSelection changes only the real controls carrying name=answer.
// It is safe to retry because checking/unchecking DOM inputs is reversible and
// does not press the external Next button.
export async function applyOptionSelection(page, optionIDs, attempts = 3) {
  const requested = new Set(optionIDs ?? []);
  if (requested.size === 0) throw new Error('at least one option id is required');

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const current = await captureChoiceQuestion(page);
    if (current.kind === 'single' && requested.size !== 1) {
      throw new Error('single-choice question requires exactly one option');
    }
    const known = new Set(current.options.map(option => option.id));
    for (const id of requested) {
      if (!known.has(id)) throw new Error(`unknown runtime option id ${id}`);
    }

    const inputs = page.locator('input[name="answer"]');
    for (let index = 0; index < await inputs.count(); index += 1) {
      const input = inputs.nth(index);
      const id = await input.inputValue();
      const wanted = requested.has(id);
      if (wanted && !(await input.isChecked())) {
        await input.check({ force: true, timeout: 3_000 });
      } else if (!wanted && current.kind === 'multiple' && await input.isChecked()) {
        await input.uncheck({ force: true, timeout: 3_000 });
      }
    }

    const selected = (await captureChoiceQuestion(page)).selected_option_ids;
    if (sameIDs(selected, [...requested])) return selected;
    if (attempt < attempts) await page.waitForTimeout(100);
  }
  throw new Error('assessment did not retain the requested option marks after 3 attempts');
}

export function sameIDs(left, right) {
  return JSON.stringify([...left].sort()) === JSON.stringify([...right].sort());
}

export function questionDOMSignature(question) {
  return JSON.stringify({
    text: normalize(question.text),
    kind: question.kind,
    options: question.options
      .map(option => `${normalize(option.id)}\u001f${normalize(option.text)}`)
      .sort(),
  });
}

function normalize(value) {
  return String(value ?? '').replace(/\s+/g, ' ').trim();
}
