package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// exportCmd downloads a YAML cluster bundle from the web console and writes it
// to a file (or stdout when --output is omitted).
func exportCmd(webBase string, args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	output := fs.String("output", "", "write bundle to file instead of stdout")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl export [--output FILE]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	data, err := webGet(webBase, "/api/export")
	if err != nil {
		die("export: %v", err)
	}

	if *output == "" {
		fmt.Print(string(data))
		return
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		die("write %s: %v", *output, err)
	}
	fmt.Fprintf(os.Stderr, "exported to %s (%d bytes)\n", *output, len(data))
}

// importCmd reads a YAML cluster bundle and applies it to the cluster via the
// web console.
func importCmd(webBase string, args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	input := fs.String("input", "", "read bundle from file instead of stdin")
	overwrite := fs.Bool("overwrite", false, "overwrite resources that already exist by name")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl import [--input FILE] [--overwrite]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	var data []byte
	var err error
	if *input != "" {
		data, err = os.ReadFile(*input)
		if err != nil {
			die("read %s: %v", *input, err)
		}
	} else {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			die("read stdin: %v", err)
		}
	}

	url := "/api/import"
	if *overwrite {
		url += "?overwrite=true"
	}
	respData, err := webPost(webBase, url, "application/yaml", data)
	if err != nil {
		die("import: %v", err)
	}

	var report struct {
		Imported map[string]int `json:"imported"`
		Skipped  []string       `json:"skipped"`
		Errors   []string       `json:"errors"`
	}
	if err := json.Unmarshal(respData, &report); err != nil {
		// Print raw response if it's not the expected JSON.
		fmt.Println(string(respData))
		return
	}

	fmt.Println("Imported:")
	for k, v := range report.Imported {
		if v > 0 {
			fmt.Printf("  %-14s %d\n", k, v)
		}
	}
	if len(report.Skipped) > 0 {
		fmt.Println("Skipped:")
		for _, s := range report.Skipped {
			fmt.Println("  •", s)
		}
	}
	if len(report.Errors) > 0 {
		fmt.Println("Errors:")
		for _, e := range report.Errors {
			fmt.Println("  ✗", e)
		}
		os.Exit(1)
	}
}

// webGet makes an authenticated GET request to the web console.
func webGet(base, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}

// webPost makes an authenticated POST request to the web console.
func webPost(base, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return respBody, nil
}
