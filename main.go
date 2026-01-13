package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"github.com/openai/openai-go/option"
	flag "github.com/spf13/pflag"
	"golang.org/x/text/language"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	lang := flag.StringP("default-lang", "l", "en", "help message for flagname")
	modelName := flag.StringP("model", "m", "gemini-2.5-flash", "translation model to use")
	provider := flag.StringP("provider", "p", "GOOGLE", "translation model provider to use (GOOGLE or VERTEXAI or OPENAI or ANTHROPIC)")
	targetLangs := flag.StringSliceP("translate-to", "t", nil, "languages to generate translations for")
	outputDir := flag.StringP("output-dir", "o", "", "directory to output the translations")
	flag.Parse()

	if *outputDir == "" {
		flag.Usage()
		log.Fatal("output-dir flag is required")
	}

	var kit *genkit.Genkit
	var model ai.Model

	switch strings.ToLower(*provider) {
	case "google":
		kit = genkit.Init(ctx, genkit.WithPlugins(&googlegenai.GoogleAI{}))
		model = googlegenai.GoogleAIModel(kit, *modelName)
	case "vertexai":
		kit = genkit.Init(ctx, genkit.WithPlugins(&googlegenai.VertexAI{}))
		model = googlegenai.VertexAIModel(kit, *modelName)
	case "openai":
		oai := &openai.OpenAI{}
		kit = genkit.Init(ctx, genkit.WithPlugins(oai))
		model = oai.Model(kit, *modelName)
	case "anthropic":
		claude := &anthropic.Anthropic{Opts: []option.RequestOption{
			option.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
		}}
		kit = genkit.Init(ctx, genkit.WithPlugins(claude))
		model = claude.Model(kit, *modelName)
	default:
		flag.Usage()
		log.Fatalf("unknown provider %q, must be one of GOOGLE, VERTEXAI, OPENAI, ANTHROPIC", *provider)
	}

	if model == nil {
		flag.Usage()
		log.Fatalf("unknown model %q for provider %q", *modelName, *provider)
	}

	fmt.Printf("using model %q from provider %q\n", model.Name(), *provider)

	if err := generate(ctx, kit, model, *lang, *outputDir, *targetLangs...); err != nil {
		log.Fatal(fmt.Errorf("generating translations: %w", err))
	}
}

func generate(ctx context.Context, kit *genkit.Genkit, model ai.Model, lang, outputDir string, targetLangs ...string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	defaultLang, err := language.Parse(lang)
	if err != nil {
		return fmt.Errorf("parsing default language %q: %w", lang, err)
	}

	defaultPath := filepath.Join(outputDir, fmt.Sprintf("active.%s.toml", defaultLang.String()))

	if err := run(
		ctx, "go", "get", "-tool", "github.com/nicksnyder/go-i18n/v2/goi18n",
	); err != nil {
		return fmt.Errorf("installing goi18n tool: %w", err)
	}

	fmt.Printf("extracting translations for %q\n", defaultLang)
	if err := run(
		ctx, "go", "tool",
		"goi18n", "extract",
		"-sourceLanguage", defaultLang.String(),
		"-format", "toml",
		"-outdir", outputDir,
	); err != nil {
		return err
	}

	mergeToTranslate := []string{
		"tool",
		"goi18n", "merge",
		"-sourceLanguage", defaultLang.String(),
		"-format", "toml",
		"-outdir", outputDir,
		defaultPath,
	}

	if len(targetLangs) > 0 {
		for _, lang := range targetLangs {
			activePath := filepath.Join(outputDir, fmt.Sprintf("active.%s.toml", lang))
			touch(activePath)

			// Clean up the existing translate file
			translatePath := filepath.Join(outputDir, fmt.Sprintf("translate.%s.toml", lang))
			if err := os.Remove(translatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("removing existing translation file %q: %w", translatePath, err)
			}

			// Generate translations for the languages
			fmt.Printf("generating required translations for %q\n", lang)
			err = run(ctx, "go", append(mergeToTranslate, activePath)...)
			if err != nil {
				return fmt.Errorf("merging translations for %q: %w", lang, err)
			}

			toTranslate, err := os.ReadFile(translatePath)
			if errors.Is(err, fs.ErrNotExist) {
				// No translations to do
				fmt.Printf("no translations needed for %q, skipping\n", lang)
				continue
			}
			if err != nil {
				return fmt.Errorf("reading translation file %q: %w", translatePath, err)
			}

			fmt.Printf("asking the model to translate %q\n", lang)
			resp, err := translate(ctx, kit, model, lang, string(toTranslate))
			if err != nil {
				return fmt.Errorf("translating: %w", err)
			}

			// overwrite the translation file with the new translations
			if err := os.WriteFile(translatePath, resp, 0o644); err != nil {
				return fmt.Errorf("writing translation file %q: %w", translatePath, err)
			}

			touch(activePath)
			fmt.Printf("merging translations for %q\n", lang)
			err = run(ctx, "go", append(mergeToTranslate, activePath, translatePath)...)
			if err != nil {
				return fmt.Errorf("merging translations for %q: %w", lang, err)
			}

			fmt.Printf("deleting the temporary translation file for %q\n", lang)
			// Clean up the translate file after merging
			if err := os.Remove(translatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("removing translation file %q: %w", translatePath, err)
			}

			fmt.Printf("translations for %q generated successfully\n", lang)
		}
	}

	fmt.Println("Translations files generated successfully")
	return nil
}

// Make sure the file exists
func touch(path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(fmt.Errorf("opening file %q: %w", path, err))
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		panic(fmt.Errorf("syncing file %q: %w", path, err))
	}
}

func run(ctx context.Context, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	c.Stderr = os.Stderr
	c.Stdout = os.Stdout
	c.Stdin = os.Stdin
	c.Cancel = func() error {
		return c.Process.Signal(syscall.SIGTERM)
	}

	err := c.Run()

	var ee *exec.ExitError
	// returns -1 if the command was closed by a signal
	if err != nil && errors.As(err, &ee) && ee.ExitCode() == -1 {
		err = nil
	}

	if err != nil {
		return fmt.Errorf(`failed to run "%s %s: %w"`, cmd, strings.Join(args, " "), err)
	}

	return nil
}

//go:embed system_prompt.md
var systemPrompt string

var messageTyp = reflect.TypeFor[Message]()

func translate(ctx context.Context, g *genkit.Genkit, model ai.Model, lang, toTranslate string) ([]byte, error) {
	current := map[string]Message{}
	if err := toml.Unmarshal([]byte(toTranslate), &current); err != nil {
		return nil, fmt.Errorf("unmarshalling translation file: %w", err)
	}
	translated := make(map[string]Message, len(current))

	var i int
	chunk := make(map[string]Message)
	for k := range current {
		i++
		if i%15 == 0 {
			translatedChunk, err := translateChunk(ctx, g, model, lang, chunk)
			if err != nil {
				return nil, fmt.Errorf("translating chunk: %w", err)
			}
			maps.Copy(translated, translatedChunk)
			chunk = make(map[string]Message)
		}
		chunk[k] = current[k]
	}

	// Translate any remaining messages in the last chunk
	translatedChunk, err := translateChunk(ctx, g, model, lang, chunk)
	if err != nil {
		return nil, fmt.Errorf("translating chunk: %w", err)
	}
	maps.Copy(translated, translatedChunk)

	// Marshal the response into a TOML format
	respToml, err := toml.Marshal(translated)
	if err != nil {
		return nil, fmt.Errorf("marshalling response to TOML: %w", err)
	}

	return respToml, nil
}

func translateChunk(ctx context.Context, g *genkit.Genkit, model ai.Model, lang string, current map[string]Message) (map[string]Message, error) {
	if len(current) == 0 {
		return nil, nil // nothing to translate
	}

	fields := make([]reflect.StructField, 0, len(current))
	for k := range current {
		fields = append(fields, reflect.StructField{
			Name: k,
			Type: messageTyp,
		})
	}

	marshalled, err := toml.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("marshalling current messages: %w", err)
	}

	resp, err := genkit.Generate(
		ctx, g,
		ai.WithModel(model),
		ai.WithSystem(systemPrompt),
		ai.WithOutputType(reflect.New(reflect.StructOf(fields)).Interface()),
		ai.WithPrompt("Translate the following text to %s:\n\n%s", lang, string(marshalled)),
	)
	if err != nil {
		return nil, fmt.Errorf("calling model: %w", err)
	}

	var value map[string]Message
	if err := resp.Output(&value); err != nil {
		return nil, fmt.Errorf("unmarshalling response: %w", err)
	}

	return value, nil
}

// Message is similar to `i18n.Message` but uses TOML tags for serialization.
// This is to prevent having empty fields in the output TOML file,
type Message struct {
	ID          string `toml:"id,omitempty"`
	Hash        string `toml:"hash,omitempty"`
	Description string `toml:"description,omitempty"`
	Zero        string `toml:"zero,omitempty"`
	One         string `toml:"one,omitempty"`
	Two         string `toml:"two,omitempty"`
	Few         string `toml:"few,omitempty"`
	Many        string `toml:"many,omitempty"`
	Other       string `toml:"other,omitempty"`
}
