package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/Darkon13/job-agent/buildinfo"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/questionbank"
)

type mockServer struct {
	seed              int64
	baseQuestionnaire core.Questionnaire
	suggestions       *core.AnswerBlock
}

type questionnaireResponse struct {
	AttemptFingerprint   string             `json:"attempt_fingerprint"`
	QuestionFingerprints map[string]string  `json:"question_fingerprints"`
	Questionnaire        core.Questionnaire `json:"questionnaire"`
}

func main() {
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("questionnaire-mock", os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	address := flag.String("listen", "127.0.0.1:8090", "HTTP listen address")
	studyBankPath := flag.String("study-bank", "", "optional imported study-bank JSON")
	flag.Parse()

	handler := newMockHandler(time.Now().UnixNano())
	if *studyBankPath != "" {
		var err error
		handler, err = newStudyMockHandler(time.Now().UnixNano(), *studyBankPath)
		if err != nil {
			log.Fatal(err)
		}
	}
	server := &http.Server{
		Addr:              *address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("questionnaire mock is ready at http://%s", *address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newMockHandler(seed int64) http.Handler {
	server := mockServer{seed: seed, baseQuestionnaire: syntheticQuestionnaire()}
	return server.handler()
}

func newStudyMockHandler(seed int64, path string) (http.Handler, error) {
	bank, err := questionbank.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fixture, err := questionbank.BuildPracticeFixture(bank)
	if err != nil {
		return nil, err
	}
	server := mockServer{
		seed: seed, baseQuestionnaire: fixture.Questionnaire, suggestions: &fixture.AnswerBlock,
	}
	log.Printf("loaded study bank %q: %d runnable questions, %d skipped", bank.Name, len(fixture.Questionnaire.Questions), fixture.Skipped)
	return server.handler(), nil
}

func (s mockServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /api/questionnaire", s.questionnaire)
	mux.HandleFunc("GET /api/study-suggestions", s.studySuggestions)
	mux.HandleFunc("POST /api/resolve", s.resolve)
	return mux
}

func (s mockServer) index(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(mockHTML))
}

func (s mockServer) questionnaire(writer http.ResponseWriter, _ *http.Request) {
	questionnaire := s.shuffledQuestionnaire()
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

func (s mockServer) studySuggestions(writer http.ResponseWriter, _ *http.Request) {
	if s.suggestions == nil {
		http.NotFound(writer, nil)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(writer).Encode(s.suggestions); err != nil {
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

	plan, err := core.ResolveAnswerBlock(s.shuffledQuestionnaire(), block)
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
	return shuffledQuestionnaire(syntheticQuestionnaire(), seed)
}

func syntheticQuestionnaire() core.Questionnaire {
	return core.Questionnaire{Title: "Синтетический тест", Questions: []core.Question{
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
	}}
}

func (s mockServer) shuffledQuestionnaire() core.Questionnaire {
	return shuffledQuestionnaire(s.baseQuestionnaire, s.seed)
}

func shuffledQuestionnaire(source core.Questionnaire, seed int64) core.Questionnaire {
	random := rand.New(rand.NewSource(seed)) //nolint:gosec // non-security mock shuffle
	questions := make([]core.Question, len(source.Questions))
	for index, question := range source.Questions {
		questions[index] = question
		questions[index].Options = append([]core.QuestionOption(nil), question.Options...)
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
	return core.Questionnaire{Title: source.Title, Questions: questions}
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
    <button id="load-study-suggestions" hidden>Подставить внешние подсказки</button>
  </div>
  <p class="muted" id="study-warning" hidden>Подсказки импортированы из внешнего учебного набора и не подтверждены результатом платформы.</p>
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
	await detectStudySuggestions();
  document.querySelector('#result').hidden = true;
  document.querySelector('#question-screen').hidden = false;
  render();
}

async function detectStudySuggestions() {
  const response = await fetch('/api/study-suggestions');
  const button = document.querySelector('#load-study-suggestions');
  button.hidden = response.status === 404;
}

function render() {
  const item = questionnaire.questions[current];
  document.querySelector('#counter').textContent = (current + 1) + ' из ' + questionnaire.questions.length;
  document.querySelector('#question').textContent = item.text;
  const options = document.querySelector('#options');
  options.replaceChildren();
	const saved = reviewed.get(normalize(item.text)) || [];
  for (const option of item.options) {
    const label = document.createElement('label');
    label.className = 'option';
    const input = document.createElement('input');
		input.type = item.kind === 'multiple' ? 'checkbox' : 'radio';
    input.name = 'answer';
    input.value = option.text;
    input.dataset.runtimeId = option.id;
		input.checked = saved.includes(normalize(option.text));
    input.addEventListener('change', () => {
			const questionKey = normalize(item.text);
			if (item.kind === 'multiple') {
				const selected = new Set(reviewed.get(questionKey) || []);
				if (input.checked) selected.add(normalize(option.text));
				else selected.delete(normalize(option.text));
				reviewed.set(questionKey, Array.from(selected));
			} else {
				reviewed.set(questionKey, [normalize(option.text)]);
			}
			document.querySelector('#next').disabled = reviewed.get(questionKey).length === 0;
    });
    label.append(input, ' ' + option.text);
    options.append(label);
  }
	document.querySelector('#next').disabled = saved.length === 0;
  document.querySelector('#progress').value = ((current + 1) / questionnaire.questions.length) * 100;
}

function finish() {
  const answers = questionnaire.questions.map(question => {
		const selected = reviewed.get(normalize(question.text)) || [];
		const options = question.options.filter(item => selected.includes(normalize(item.text)));
    return {
      question: question.text,
      question_fingerprint: questionFingerprints[question.id],
			selected_options: options.map(option => option.text)
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
	await loadAnswerBlock(answerBlock);
});

async function loadAnswerBlock(answerBlock) {
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
		const options = question.options
			.filter(item => resolved.selected_option_ids.includes(item.id))
			.map(item => normalize(item.text));
		reviewed.set(normalize(question.text), options);
  }
  render();
}

document.querySelector('#load-study-suggestions').addEventListener('click', async () => {
	const response = await fetch('/api/study-suggestions');
	if (!response.ok) {
		alert(await response.text());
		return;
	}
	await loadAnswerBlock(await response.json());
	document.querySelector('#study-warning').hidden = false;
});

document.querySelector('#restart').addEventListener('click', loadQuestionnaire);
loadQuestionnaire();
</script>
</body>
</html>`
