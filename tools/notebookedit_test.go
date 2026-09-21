package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func makeTestNotebook(t *testing.T, dir string) string {
	t.Helper()
	nb := map[string]interface{}{
		"nbformat": 4, "nbformat_minor": 5,
		"cells": []interface{}{
			map[string]interface{}{
				"cell_type":       "code",
				"source":          []string{"print('hello')"},
				"outputs":         []interface{}{},
				"execution_count": nil,
				"metadata":        map[string]interface{}{},
			},
		},
		"metadata": map[string]interface{}{},
	}
	data, _ := json.Marshal(nb)
	path := filepath.Join(dir, "test.ipynb")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNotebookEditTool(t *testing.T) {
	dir := t.TempDir()
	nbPath := makeTestNotebook(t, dir)
	tool := NewNotebookEditTool()
	ctx := testToolCtx(t)

	// insert markdown cell at position 0
	r, err := tool.Call(context.Background(), map[string]interface{}{
		"file_path":   nbPath,
		"command":     "insert",
		"cell_number": float64(0),
		"cell_type":   "markdown",
		"source":      "# Title",
	}, ctx)
	if err != nil || r.IsError {
		t.Fatalf("insert failed: %v, text: %s", err, r.Content[0].Text)
	}

	// replace cell 0
	r, err = tool.Call(context.Background(), map[string]interface{}{
		"file_path":   nbPath,
		"command":     "replace",
		"cell_number": float64(0),
		"source":      "# Updated",
	}, ctx)
	if err != nil || r.IsError {
		t.Fatalf("replace failed: %v", err)
	}

	// delete cell 0
	r, err = tool.Call(context.Background(), map[string]interface{}{
		"file_path":   nbPath,
		"command":     "delete",
		"cell_number": float64(0),
	}, ctx)
	if err != nil || r.IsError {
		t.Fatalf("delete failed: %v", err)
	}

	// delete out of bounds
	r, _ = tool.Call(context.Background(), map[string]interface{}{
		"file_path":   nbPath,
		"command":     "delete",
		"cell_number": float64(99),
	}, ctx)
	if !r.IsError {
		t.Error("expected error for out-of-bounds cell")
	}
}
