# Autotranslate

This is a wrapper on top of [go-i18n](github.com/nicksnyder/go-i18n).

Given the default language and the languages to translate to, it will use your AI model of choice to generate the translations.

## Usage

```sh
go get -tool github.com/go-swiss/autotranslate
go tool autotranslate --default-lang en --translate-to fr,de,es --output-dir ./translations/
```

```sh
  -l, --default-lang string    help message for flagname (default "en")
  -m, --model string           translation model to use (default "gemini-2.5-flash")
  -o, --output-dir string      directory to output the translations
  -p, --provider string        translation model provider to use (GOOGLE or VERTEXAI or OPENAI or ANTHROPIC) (default "GOOGLE")
  -t, --translate-to strings   languages to generate translations for
```

## Configuration

### Provider

The default provider is "GOOGLE", but this can be changed by passing the `--provider` flag. Options are:

- **google**: Set the `GEMINI_API_KEY` environment variable.
- **openai**: Set the `OPENAI_API_KEY` environment variable.
- **anthropic**: Set the `ANTHROPIC_API_KEY` environment variable.
- **vertexai**: Set the `GOOGLE_CLOUD_PROJECT` and `GOOGLE_CLOUD_LOCATION` environment variables. Also ensure the Google Cloud Application Default Credentials are set up, which can be done by running `gcloud auth application-default login`.

### Model

The default model is `gemini-2.5-flash`, but this can be changed by passing the `--model` flag. The available model depends on the provider.
