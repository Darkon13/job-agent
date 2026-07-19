package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/Darkon13/job-agent/core"
)

type mockServer struct {
	seed int64
}

type questionnaireResponse struct {
	AttemptFingerprint   string             `json:"attempt_fingerprint"`
	QuestionFingerprints map[string]string  `json:"question_fingerprints"`
	Questionnaire        core.Questionnaire `json:"questionnaire"`
}

func main() {
	address := flag.String("listen", "127.0.0.1:8090", "HTTP listen address")
	flag.Parse()

	server := &http.Server{
		Addr:              *address,
		Handler:           newMockHandler(time.Now().UnixNano()),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("questionnaire mock is ready at http://%s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newMockHandler(seed int64) http.Handler {
	server := mockServer{seed: seed}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.index)
	mux.HandleFunc("GET /api/questionnaire", server.questionnaire)
	mux.HandleFunc("POST /api/resolve", server.resolve)
	return mux
}

func (s mockServer) index(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(mockHTML))
}

func (s mockServer) questionnaire(writer http.ResponseWriter, _ *http.Request) {
	questionnaire := shuffledMockQuestionnaire(s.seed)
	attemptFingerprint, err := core.ObservedAttemptFingerprint(questionnaire)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	questionFingerprints := make(map[string]string, len(questionnaire.Questions))
	for _, question := range questionnaire.Questions {
		fingerprint, err := core.QuestionFingerprint(question)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		questionFingerprints[question.ID] = fingerprint
	}
	if err := json.NewEncoder(writer).Encode(questionnaireResponse{
		AttemptFingerprint: attemptFingerprint, QuestionFingerprints: questionFingerprints,
		Questionnaire: questionnaire,
	}); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (s mockServer) resolve(writer http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var block core.AnswerBlock
	if err := decoder.Decode(&block); err != nil {
		http.Error(writer, fmt.Sprintf("decode answer block: %v", err), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(writer, "answer block must contain exactly one JSON value", http.StatusBadRequest)
		return
	}

	plan, err := core.ResolveAnswerBlock(shuffledMockQuestionnaire(s.seed), block)
	if err != nil {
		http.Error(writer, fmt.Sprintf("resolve answer block: %v", err), http.StatusUnprocessableEntity)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(writer).Encode(plan); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func shuffledMockQuestionnaire(seed int64) core.Questionnaire {
	random := rand.New(rand.NewSource(seed)) //nolint:gosec // non-security mock shuffle
	questions := []core.Question{
		{
			Text: "Вопрос 1: выберите один вариант",
			Kind: core.QuestionSingle,
			Options: []core.QuestionOption{
				{Text: "Ответ А"},
				{Text: "Ответ Б"},
				{Text: "Ответ В"},
				{Text: "Ответ Г"},
			},
		},
		{
			Text: "Вопрос 2: выберите другой вариант",
			Kind: core.QuestionSingle,
			Options: []core.QuestionOption{
				{Text: "Вариант А"},
				{Text: "Вариант Б"},
				{Text: "Вариант В"},
				{Text: "Вариант Г"},
				{Text: "Вариант Д"},
			},
		},
	}

	for questionIndex := range questions {
		questions[questionIndex].ID = fmt.Sprintf("question-%d-%d", questionIndex, random.Uint64())
		for optionIndex := range questions[questionIndex].Options {
			questions[questionIndex].Options[optionIndex].ID = fmt.Sprintf("option-%d-%d-%d", questionIndex, optionIndex, random.Uint64())
		}
		random.Shuffle(len(questions[questionIndex].Options), func(i, j int) {
			questions[questionIndex].Options[i], questions[questionIndex].Options[j] = questions[questionIndex].Options[j], questions[questionIndex].Options[i]
		})
	}
	random.Shuffle(len(questions), func(i, j int) {
		questions[i], questions[j] = questions[j], questions[i]
	})
	return core.Questionnaire{Title: "Синтетический тест", Questions: questions}
}

const mockHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Questionnaire mock</title>
  <style>
    body { font: 16px system-ui, sans-serif; max-width: 760px; margin: 48px auto; padding: 0 20px; color: #202124; }
    .toolbar { display: flex; gap: 12px; align-items: center; margin-bottom: 24px; }
    .option { display: block; border: 1px solid #d6dbe3; border-radius: 14px; padding: 16px; margin: 10px 0; cursor: pointer; }
    .option:has(input:checked) { border-color: #6c5ce7; background: #f3f0ff; }
    button { padding: 11px 20px; border: 0; border-radius: 10px; cursor: pointer; background: #6c5ce7; color: white; }
    button:disabled { cursor: default; opacity: .45; }
    progress { width: 100%; margin-top: 24px; }
    pre { white-space: pre-wrap; background: #f4f5f7; border-radius: 12px; padding: 16px; }
    .muted { color: #68707d; }
  </style>
</head>
<body>
  <h1>Questionnaire mock</h1>
  <div class="toolbar">
    <label>Загрузить AnswerBlock: <input id="answer-block-file" type="file" accept="application/json"></label>
  </div>
  <main id="question-screen" hidden>
    <p class="muted" id="counter"></p>
    <h2 id="question"></h2>
    <div id="options"></div>
    <button id="next" data-qa="footer-next-button" disabled>Дальше</button>
    <progress id="progress" data-qa="progress" max="100" value="0"></progress>
  </main>
  <section id="result" hidden>
    <h2>Сохранённая карта ответов</h2>
    <p class="muted">Её можно загрузить при следующем запуске, даже если вопросы и варианты перемешались.</p>
    <pre id="answer-block-json"></pre>
    <button id="restart">Пройти mock ещё раз</button>
  </section>
<script>
let questionnaire;
let questionFingerprints;
let current = 0;
let reviewed = new Map();

const normalize = value => value.trim().toLocaleLowerCase('ru-RU').replace(/\s+/g, ' ');

async function loadQuestionnaire() {
  const response = await fetch('/api/questionnaire');
  const payload = await response.json();
  questionnaire = payload.questionnaire;
  questionFingerprints = payload.question_fingerprints;
  current = 0;
  document.querySelector('#result').hidden = true;
  document.querySelector('#question-screen').hidden = false;
  render();
}

function render() {
  const item = questionnaire.questions[current];
  document.querySelector('#counter').textContent = (current + 1) + ' из ' + questionnaire.questions.length;
  document.querySelector('#question').textContent = item.text;
  const options = document.querySelector('#options');
  options.replaceChildren();
  const saved = reviewed.get(normalize(item.text));
  for (const option of item.options) {
    const label = document.createElement('label');
    label.className = 'option';
    const input = document.createElement('input');
    input.type = 'radio';
    input.name = 'answer';
    input.value = option.text;
    input.dataset.runtimeId = option.id;
    input.checked = saved === normalize(option.text);
    input.addEventListener('change', () => {
      reviewed.set(normalize(item.text), normalize(option.text));
      document.querySelector('#next').disabled = false;
    });
    label.append(input, ' ' + option.text);
    options.append(label);
  }
  document.querySelector('#next').disabled = !saved;
  document.querySelector('#progress').value = ((current + 1) / questionnaire.questions.length) * 100;
}

function finish() {
  const answers = questionnaire.questions.map(question => {
    const selected = reviewed.get(normalize(question.text));
    const option = question.options.find(item => normalize(item.text) === selected);
    return {
      question: question.text,
      question_fingerprint: questionFingerprints[question.id],
      selected_options: [option.text]
    };
  });
  const answerBlock = {
    tag: 'mock-reviewed',
    name: 'Синтетический qualification block',
    kind: 'qualification',
    platform: 'mock',
    match: {},
    answers
  };
  document.querySelector('#question-screen').hidden = true;
  document.querySelector('#result').hidden = false;
  document.querySelector('#answer-block-json').textContent = JSON.stringify(answerBlock, null, 2);
}

document.querySelector('#next').addEventListener('click', () => {
  if (current + 1 === questionnaire.questions.length) finish();
  else { current += 1; render(); }
});

document.querySelector('#answer-block-file').addEventListener('change', async event => {
  const file = event.target.files[0];
  if (!file) return;
  const answerBlock = JSON.parse(await file.text());
  const response = await fetch('/api/resolve', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(answerBlock)
  });
  if (!response.ok) {
    alert(await response.text());
    return;
  }
  const plan = await response.json();
  reviewed = new Map();
  for (const resolved of plan.answers) {
    const question = questionnaire.questions.find(item => item.id === resolved.question_id);
    const option = question.options.find(item => resolved.selected_option_ids.includes(item.id));
    reviewed.set(normalize(question.text), normalize(option.text));
  }
  render();
});

document.querySelector('#restart').addEventListener('click', loadQuestionnaire);
loadQuestionnaire();
</script>
</body>
</html>`
