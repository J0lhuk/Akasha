module akasha

go 1.26.4

require (
	github.com/amikos-tech/pure-tokenizers v0.1.4
	github.com/benedoc-inc/onnxer v0.7.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/tiktoken-go/tokenizer v0.8.1
	go.yaml.in/yaml/v3 v3.0.4
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
	modernc.org/sqlite v1.60.1
)

require (
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/dlclark/regexp2/v2 v2.5.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.9.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/benedoc-inc/onnxer => ./third_party/onnxer
