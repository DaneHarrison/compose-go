/*
   Copyright 2020 The Compose Specification Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package tests

// The tests in this file lock the `env_file` attribute of an `include` entry:
//   https://github.com/compose-spec/compose-spec/blob/main/14-include.md#env_file
//
// Spec: "`env_file` defines an environment file(s) to use to define default
// values when interpolating variables in the Compose file being parsed."
//
// An entry accepts the same short (path) and long ({path, required}) syntax
// as the service-level `env_file`: a missing file is an error unless the
// entry sets `required: false`, and `required` defaults to true. `format`
// selects the parser registered with dotenv.RegisterFormat, as it does for a
// service env_file.
//
// The `include` block is resolved and dropped while loading, so the observable
// effect is the interpolated value in the included service, not a field of the
// loaded project; these tests therefore do not round-trip.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"gotest.tools/v3/assert"
)

// includeEnvFiles holds slash-separated absolute paths to the env files
// written by includeEnvFileFixture.
type includeEnvFiles struct {
	// first sets IMAGE=first and TAG=first
	first string
	// second sets TAG=second only
	second string
	// missing does not exist
	missing string
}

// includeEnvFileFixture writes an included compose file whose image is
// interpolated as ${IMAGE:-default}:${TAG:-latest}, plus the env files
// described by includeEnvFiles, and returns the compose file path and the env
// file paths, all slash-separated and absolute.
func includeEnvFileFixture(t *testing.T) (string, includeEnvFiles) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) string {
		f := filepath.Join(dir, name)
		assert.NilError(t, os.WriteFile(f, []byte(content), 0o600))
		return filepath.ToSlash(f)
	}
	composeFile := write("compose.yaml", `
services:
  app:
    image: ${IMAGE:-default}:${TAG:-latest}
`)
	return composeFile, includeEnvFiles{
		first:   write("first.env", "IMAGE=first\nTAG=first\n"),
		second:  write("second.env", "TAG=second\n"),
		missing: filepath.ToSlash(filepath.Join(dir, "missing.env")),
	}
}

func TestIncludeEnvFileRequired(t *testing.T) {
	// an include env_file, whatever its syntax and `required` value, must feed
	// interpolation of the included file when it exists; when it is missing,
	// loading must fail unless `required: false`, in which case it is skipped.
	tests := []struct {
		name string
		// envFile renders the env_file value from the fixture's paths
		envFile func(f includeEnvFiles) string
		// wantImage is the interpolated image; empty means loading must fail
		wantImage string
	}{
		{
			name:      "single string, file present",
			envFile:   func(f includeEnvFiles) string { return yamlQuote(f.first) },
			wantImage: "first:first",
		},
		{
			name:    "single string, file missing",
			envFile: func(f includeEnvFiles) string { return yamlQuote(f.missing) },
		},
		{
			name:      "list of strings, file present",
			envFile:   func(f includeEnvFiles) string { return yamlList(yamlQuote(f.first)) },
			wantImage: "first:first",
		},
		{
			name:    "list of strings, file missing",
			envFile: func(f includeEnvFiles) string { return yamlList(yamlQuote(f.missing)) },
		},
		{
			name:      "required omitted, file present",
			envFile:   func(f includeEnvFiles) string { return yamlList(envFileObj(f.first, "")) },
			wantImage: "first:first",
		},
		{
			name:    "required omitted, file missing",
			envFile: func(f includeEnvFiles) string { return yamlList(envFileObj(f.missing, "")) },
		},
		{
			name:      "required true, file present",
			envFile:   func(f includeEnvFiles) string { return yamlList(envFileObj(f.first, "true")) },
			wantImage: "first:first",
		},
		{
			name:    "required true, file missing",
			envFile: func(f includeEnvFiles) string { return yamlList(envFileObj(f.missing, "true")) },
		},
		{
			name:      "required false, file present",
			envFile:   func(f includeEnvFiles) string { return yamlList(envFileObj(f.first, "false")) },
			wantImage: "first:first",
		},
		{
			name:      "required false, file missing",
			envFile:   func(f includeEnvFiles) string { return yamlList(envFileObj(f.missing, "false")) },
			wantImage: "default:latest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIncludeEnvFile(t, tt.envFile, tt.wantImage)
		})
	}
}

func TestIncludeEnvFileList(t *testing.T) {
	// every object of an include env_file list must feed interpolation, a file
	// listed later overriding the variables of an earlier one; each entry's
	// `required` is honored independently, so a missing required entry fails
	// the load even when other entries exist, while a missing optional entry
	// is skipped.
	tests := []struct {
		name string
		// envFile renders the env_file value from the fixture's paths
		envFile func(f includeEnvFiles) string
		// wantImage is the interpolated image; empty means loading must fail
		wantImage string
	}{
		{
			name: "every file is read, later file overrides earlier",
			envFile: func(f includeEnvFiles) string {
				return yamlList(envFileObj(f.first, ""), envFileObj(f.second, ""))
			},
			wantImage: "first:second",
		},
		{
			name: "optional missing entry is skipped",
			envFile: func(f includeEnvFiles) string {
				return yamlList(envFileObj(f.first, "true"), envFileObj(f.missing, "false"))
			},
			wantImage: "first:first",
		},
		{
			name: "required missing entry fails despite a present one",
			envFile: func(f includeEnvFiles) string {
				return yamlList(envFileObj(f.first, "true"), envFileObj(f.missing, "true"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertIncludeEnvFile(t, tt.envFile, tt.wantImage)
		})
	}
}

func TestIncludeEnvFileFormat(t *testing.T) {
	// an include env_file `format` must select the parser the file is read
	// with, and an unregistered format must fail the load rather than fall
	// back to the .env syntax.
	dotenv.RegisterFormat("include-test", func(r io.Reader, _ string, vars map[string]string, _ func(string) (string, bool)) error {
		// the whole file content is the IMAGE value
		b, err := io.ReadAll(r)
		vars["IMAGE"] = strings.TrimSpace(string(b))
		return err
	})

	t.Run("registered format is used", func(t *testing.T) {
		composeFile, files := includeEnvFileFixture(t)
		raw := filepath.ToSlash(filepath.Join(filepath.Dir(files.first), "vars.raw"))
		assert.NilError(t, os.WriteFile(raw, []byte("custom\n"), 0o600))
		p := load(t, fmt.Sprintf(`
name: test
include:
  - path: %s
    env_file:
      - path: %s
        format: include-test
`, yamlQuote(composeFile), yamlQuote(raw)))
		assert.Equal(t, p.Services["app"].Image, "custom:latest")
	})

	t.Run("unregistered format fails", func(t *testing.T) {
		composeFile, files := includeEnvFileFixture(t)
		err := loadErr(t, fmt.Sprintf(`
name: test
include:
  - path: %s
    env_file:
      - path: %s
        format: unknown
`, yamlQuote(composeFile), yamlQuote(files.first)))
		assert.ErrorContains(t, err, `unsupported env_file format "unknown"`)
	})
}

// assertIncludeEnvFile loads a project including the fixture's compose file
// with the rendered env_file value, and asserts the interpolated image, or,
// when wantImage is empty, that loading fails on the missing env file.
func assertIncludeEnvFile(t *testing.T, envFile func(f includeEnvFiles) string, wantImage string) {
	t.Helper()
	composeFile, files := includeEnvFileFixture(t)
	yaml := fmt.Sprintf(`
name: test
include:
  - path: %s
    env_file: %s
`, yamlQuote(composeFile), envFile(files))

	if wantImage == "" {
		err := loadErr(t, yaml)
		assert.ErrorContains(t, err, "missing.env")
		assert.Assert(t, errors.Is(err, fs.ErrNotExist), err)
		return
	}
	p := load(t, yaml)
	assert.Equal(t, p.Services["app"].Image, wantImage)
}

// yamlQuote renders a path as a single-quoted YAML scalar.
func yamlQuote(path string) string {
	return fmt.Sprintf("'%s'", path)
}

// envFileObj renders a long-syntax env_file entry; an empty required omits the key.
func envFileObj(path, required string) string {
	if required == "" {
		return fmt.Sprintf("{path: %s}", yamlQuote(path))
	}
	return fmt.Sprintf("{path: %s, required: %s}", yamlQuote(path), required)
}

// yamlList renders entries as a YAML flow sequence.
func yamlList(entries ...string) string {
	return "[" + strings.Join(entries, ", ") + "]"
}
