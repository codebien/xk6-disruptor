// main is the main package for depsync, a script that checks and provides commands to synchronize
// common dependencies with k6 core.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

const (
	k6Core     = "go.k6.io/k6"
	k6GoModURL = "https://proxy.golang.org/go.k6.io/k6/@v/%s.mod"
)

func main() {
	gomod := "go.mod"
	if len(os.Args) >= 2 {
		gomod = os.Args[1]
	}

	file, err := os.Open(gomod)
	if err != nil {
		log.Fatalf("opening go.mod: %v", err)
	}

	ownDeps, err := dependencies(file)
	if err != nil {
		log.Fatalf("reading dependencies: %v", err)
	}

	k6Version := ownDeps[k6Core]
	log.Printf("detected k6 core version %s", k6Version)

	k6CoreVersionedURL := fmt.Sprintf(k6GoModURL, k6Version)
	//nolint:bodyclose // Single-run script.
	response, err := http.Get(k6CoreVersionedURL)
	if err != nil {
		log.Fatalf("error fetching k6 go.mod: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		log.Fatalf("got HTTP status %d for %s", response.StatusCode, k6CoreVersionedURL)
	}

	coreDeps, err := dependencies(response.Body)
	if err != nil {
		log.Fatalf("reading k6 core dependencies: %v", err)
	}

	var mismatched []string
	for dep, version := range ownDeps {
		coreVersion, inCore := coreDeps[dep]
		if !inCore {
			continue
		}

		if version == coreVersion {
			continue
		}

		log.Printf("Mismatched versions for %s: %s (ours) -> %s (core)", dep, version, coreVersion)
		mismatched = append(mismatched, fmt.Sprintf("%s@%s", dep, coreVersion))
	}

	if len(mismatched) == 0 {
		log.Println("All deps are in sync, nothing to do.")
		return
	}

	// TODO: Use slices.Sort when we move to a go version that has it on the stdlib.
	sort.Strings(mismatched)

	//nolint:forbidigo // We are willingly writing to stdout here.
	fmt.Printf("go get %s\n", strings.Join(mismatched, " "))
}

func dependencies(reader io.Reader) (map[string]string, error) {
	buf := bufio.NewReader(reader)

	deps := make(map[string]string)
	for {
		line, err := buf.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return deps, nil
		}

		if err != nil {
			return nil, fmt.Errorf("reading go.mod: %w", err)
		}

		if !strings.HasPrefix(line, "\t") {
			continue
		}

		line = strings.TrimPrefix(line, "\t")
		line = strings.TrimSuffix(line, "\n")
		pieces := strings.Split(line, " ")
		deps[pieces[0]] = pieces[1]
	}
}
