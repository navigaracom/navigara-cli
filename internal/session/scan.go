package session

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Result counts attempted regular history files separately from root diagnostics.
// Files = len(Records) + Skipped + per-file failures.
type Result struct {
	Records []Record
	Errors  []error
	Skipped int
	Files   int
}

// Scan discovers local Claude and Codex histories in fixed source/root order.
// Known roots may be symlinks; symlinks and nonregular entries found inside them
// are ignored. Each opened file is bounded to its initial size, excluding appends
// but allowing concurrent rewrites or truncation to affect the read. A history that skip
// reports, when skip is set, is neither read nor counted.
func Scan(source, claudeHome, codexHome string, skip func(path string) bool) Result {
	result := Result{Records: []Record{}}
	if source != "all" && source != "claude" && source != "codex" {
		result.Errors = append(result.Errors, errors.New("unsupported source"))
		return result
	}
	for _, tool := range []struct {
		name, reader, home string
		roots              []string
	}{
		{"claude", "claude_code", claudeHome, []string{"projects"}},
		{"codex", "codex", codexHome, []string{"sessions", "archived_sessions"}},
	} {
		if source != "all" && source != tool.name {
			continue
		}
		home := tool.home
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				result.Errors = append(result.Errors, pathDiagnostic("."+tool.name, err))
				continue
			}
			home = filepath.Join(userHome, "."+tool.name)
		}
		info, err := os.Stat(home)
		if err != nil {
			if tool.home != "" || !errors.Is(err, fs.ErrNotExist) {
				result.Errors = append(result.Errors, pathDiagnostic(home, err))
			}
			continue
		}
		if !info.IsDir() {
			result.Errors = append(result.Errors, pathDiagnostic(home, errors.New("not a directory")))
			continue
		}
		for _, suffix := range tool.roots {
			result.walk(tool.reader, filepath.Join(home, suffix), skip)
		}
	}
	return result
}

func (result *Result) walk(source, root string, skip func(path string) bool) {
	resolved, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		var info fs.FileInfo
		info, err = os.Stat(resolved)
		if err == nil && !info.IsDir() {
			err = errors.New("not a directory")
		}
	}
	if err != nil {
		result.Errors = append(result.Errors, pathDiagnostic(root, err))
		return
	}
	filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, pathDiagnostic(path, err))
			return nil
		}
		rel, _ := filepath.Rel(resolved, path)
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if source == "claude_code" {
			if entry.IsDir() && rel != "." && depth > 1 {
				return filepath.SkipDir
			}
			if depth != 2 || strings.HasPrefix(entry.Name(), "agent-") {
				return nil
			}
		}
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".jsonl" || (skip != nil && skip(path)) {
			return nil
		}
		result.Files++
		record, err := readFile(source, path)
		if err != nil {
			result.Errors = append(result.Errors, pathDiagnostic(path, err))
		} else if record.Session.SessionID == "" {
			result.Skipped++
		} else {
			record.Path = path
			result.Records = append(result.Records, record)
		}
		return nil
	})
}

func readFile(source, path string) (record Record, err error) {
	f, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			record, err = Record{}, errors.Join(err, closeErr)
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() {
		return Record{}, errors.New("not a regular file")
	}
	return readSnapshot(source, f, info.Size())
}

func readSnapshot(source string, r io.Reader, size int64) (Record, error) {
	return Read(source, io.LimitReader(r, size))
}

func pathDiagnostic(path string, err error) error {
	var pathErr *os.PathError
	for errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("%q: %q", path, err.Error())
}
