# Tools CLI

A flexible command-line interface built with Go and Cobra, providing various utilities and tools for development and automation tasks.

## Features

- Built with Go and the Cobra CLI framework
- Environment-based configuration using `.env` files
- Modular architecture ready for adding subcommands
- Comprehensive configuration management for cloud services
- Support for OpenAI API, Google Cloud, and Google Sheets integration

## Project Structure

```
.
├── .env.example          # Environment variables template
├── .gitignore           # Git ignore patterns
├── go.mod               # Go module definition
├── go.sum               # Go module checksums
├── README.md            # This file
├── main.go              # Application entry point
├── cmd/                 # CLI commands
│   └── root.go          # Root command definition
├── internal/            # Private application code
│   └── config/          # Configuration management
│       └── config.go    # Config struct and loading logic
└── pkg/                 # Public library code (empty)
    └── .gitkeep
```

## Setup

### Prerequisites

- Go 1.19 or later
- Git

### Installation

1. **Clone or set up the project:**
   ```bash
   # If cloning from a repository
   git clone <repository-url>
   cd tools
   
   # Or if starting fresh
   mkdir tools && cd tools
   ```

2. **Set up environment variables:**
   ```bash
   cp .env.example .env
   ```
   
   Edit `.env` with your actual configuration values. The file includes detailed comments explaining each variable.

3. **Install dependencies:**
   ```bash
   go mod tidy
   ```

4. **Build the application:**
   ```bash
   go build -o tools
   ```

## Usage

### Basic Commands

```bash
# Show help
./tools --help

# Show version
./tools --version

# Run the default command
./tools
```

### Environment Configuration

The application loads configuration from:
1. `.env` file (if present)
2. Environment variables
3. Default values

Required environment variables:
- `OPENAI_API_KEY` - Your OpenAI API key
- `GOOGLE_CLOUD_PROJECT` - Google Cloud project ID
- `GCS_SOURCE_BUCKET` - Source storage bucket
- `GCS_OUTPUT_BUCKET` - Output storage bucket
- `DOCUMENT_AI_PROCESSOR_ID` - Document AI processor ID
- `GOOGLE_SHEET_URL` - Google Sheets URL for exports

## Development

### Adding New Commands

To add a new subcommand:

1. Create a new file in the `cmd/` directory (e.g., `cmd/newcommand.go`)
2. Define your command using Cobra conventions
3. Add the command to the root command in the `init()` function

Example:
```go
package cmd

import (
    "fmt"
    "github.com/spf13/cobra"
)

var newCmd = &cobra.Command{
    Use:   "new",
    Short: "A brief description of your command",
    Run: func(cmd *cobra.Command, args []string) {
        fmt.Println("New command executed!")
    },
}

func init() {
    rootCmd.AddCommand(newCmd)
}
```

### Configuration Management

The configuration is managed through the `internal/config` package:

- `Config` struct defines all configuration options
- `Load()` function loads configuration from environment
- `validate()` ensures required fields are present

### Testing

```bash
# Run tests
go test ./...

# Run tests with coverage
go test -cover ./...
```

### Building for Different Platforms

```bash
# Build for Linux
GOOS=linux GOARCH=amd64 go build -o tools-linux

# Build for macOS
GOOS=darwin GOARCH=amd64 go build -o tools-macos

# Build for Windows
GOOS=windows GOARCH=amd64 go build -o tools.exe
```

## Dependencies

- [Cobra](https://github.com/spf13/cobra) - CLI framework
- [godotenv](https://github.com/joho/godotenv) - Environment variable loading

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request