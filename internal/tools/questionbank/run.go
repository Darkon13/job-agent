package questionbank

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Darkon13/job-agent/buildinfo"
	"github.com/Darkon13/job-agent/core"
	"github.com/Darkon13/job-agent/questionbank"
)

const defaultRepository = "https://github.com/Londeren/hh-skill-verifications-quizzes"

type importOptions struct {
	source     string
	output     string
	repository string
	revision   string
	license    string
}

type importedBank struct {
	relativePath string
	bank         questionbank.Bank
	report       questionbank.ImportReport
}

func runMain() {
	if buildinfo.Requested(os.Args[1:]) {
		if err := buildinfo.Write("job-agent-question-bank-import", os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("job-agent-question-bank-import", flag.ContinueOnError)
	flags.SetOutput(stdout)
	options := importOptions{}
	flags.StringVar(&options.source, "source", "", "path to the checked-out Markdown repository")
	flags.StringVar(&options.output, "out", "", "output directory for local study-bank JSON")
	flags.StringVar(&options.repository, "repository", defaultRepository, "source repository URL")
	flags.StringVar(&options.revision, "revision", "", "exact source Git revision")
	flags.StringVar(&options.license, "license", "AGPL-3.0-only", "source license identifier")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if options.source == "" || options.output == "" || options.revision == "" {
		return errors.New("-source, -out and -revision are required")
	}

	imports, err := readBanks(options)
	if err != nil {
		return err
	}
	if len(imports) == 0 {
		return errors.New("source does not contain importable Markdown banks")
	}
	if err := writeBanks(options.output, imports); err != nil {
		return err
	}

	var questions, complete, incomplete int
	for _, imported := range imports {
		questions += imported.report.Questions
		complete += imported.report.CompleteQuestions
		incomplete += imported.report.IncompleteQuestions
	}
	_, err = fmt.Fprintf(stdout,
		"imported %d banks: %d questions, %d complete, %d answer-only; verification=%s\n",
		len(imports), questions, complete, incomplete, questionbank.VerificationExternalUnknown,
	)
	return err
}

func readBanks(options importOptions) ([]importedBank, error) {
	var paths []string
	err := filepath.WalkDir(options.source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		relative, err := filepath.Rel(options.source, path)
		if err != nil {
			return err
		}
		if !strings.Contains(relative, string(filepath.Separator)) {
			return nil
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source: %w", err)
	}
	sort.Strings(paths)

	imports := make([]importedBank, 0, len(paths))
	for _, relative := range paths {
		imported, err := readBank(options, relative)
		if err != nil {
			return nil, err
		}
		imports = append(imports, imported)
	}
	return imports, nil
}

func readBank(options importOptions, relative string) (importedBank, error) {
	path := filepath.Join(options.source, relative)
	file, err := os.Open(path)
	if err != nil {
		return importedBank{}, fmt.Errorf("open %s: %w", relative, err)
	}
	defer file.Close()

	parts := strings.Split(filepath.ToSlash(relative), "/")
	familyID := parts[0]
	levelID := strings.TrimSuffix(parts[len(parts)-1], filepath.Ext(parts[len(parts)-1]))
	tag := "hh-community-" + strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative)), "/", "-")
	bank, report, err := questionbank.ParseMarkdown(file, questionbank.ImportMetadata{
		Tag: tag, Platform: core.Platform("study"), TargetPlatform: core.Platform("hh"),
		Qualification: core.QualificationDescriptor{
			FamilyID: "community:" + familyID,
			LevelID:  "community:" + levelID,
		},
		Source: questionbank.Source{
			Repository: options.repository, Revision: options.revision,
			Path: filepath.ToSlash(relative), License: options.license,
		},
	})
	if err != nil {
		return importedBank{}, fmt.Errorf("parse %s: %w", relative, err)
	}
	return importedBank{
		relativePath: strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative)) + ".json",
		bank:         bank, report: report,
	}, nil
}

func writeBanks(output string, imports []importedBank) error {
	for _, imported := range imports {
		path := filepath.Join(output, filepath.FromSlash(imported.relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
		temporary, err := os.CreateTemp(filepath.Dir(path), ".study-bank-*.json")
		if err != nil {
			return fmt.Errorf("create temporary study bank: %w", err)
		}
		encoder := json.NewEncoder(temporary)
		encoder.SetIndent("", "  ")
		encodeErr := encoder.Encode(imported.bank)
		closeErr := temporary.Close()
		if encodeErr != nil {
			return fmt.Errorf("encode %s: %w", imported.relativePath, encodeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", imported.relativePath, closeErr)
		}
		if err := os.Rename(temporary.Name(), path); err != nil {
			return fmt.Errorf("publish %s: %w", imported.relativePath, err)
		}
	}
	return nil
}

// Run executes the tool with the given arguments and returns a process exit
// code. It preserves the historical CLI contract of cmd/job-agent-question-bank-import.
func Run(args []string) int {
	os.Args = append([]string{os.Args[0]}, args...)
	runMain()
	return 0
}
