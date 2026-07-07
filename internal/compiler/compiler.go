// Package compiler translates a pipeline definition file (JSON or YAML) into
// Forge's canonical pipeline IR (internal/pipeline types).
package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

// scriptInterpreter returns the interpreter for a script file based on its extension.
func scriptInterpreter(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python3"
	case ".sh", ".bash":
		return "sh"
	case ".js", ".mjs":
		return "node"
	case ".rb":
		return "ruby"
	case ".ts":
		return "ts-node"
	default:
		return "sh"
	}
}

type jsonPipeline struct {
	Name  string     `json:"name"`
	Steps []jsonStep `json:"steps"`
}

type jsonStep struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Run          string            `json:"run"`
	Command      []string          `json:"command"`
	WorkDir      string            `json:"workdir"`
	Env          map[string]string `json:"env"`
	DependsOn    []string          `json:"depends_on"`
	Inputs       []string          `json:"inputs"`
	Timeout      string            `json:"timeout"`
	Secrets      []string          `json:"secrets"`
	Type         string            `json:"type"` // "task" (default) | "generator"
	DockerSocket bool              `json:"docker_socket"`
	// script: runs an external file from the workspace rather than inlining shell/python.
	// The interpreter is inferred from the file extension:
	//   .py  → python3    .sh → sh    .js → node    .rb → ruby
	// Path is relative to the workspace root and is resolved to /workspace/<path>
	// inside the container at compile time.
	Script string `json:"script,omitempty"`
	// Pipeline chaining fields — used when type == "pipeline"
	Pipeline         string            `json:"pipeline,omitempty"`
	Wait             *bool             `json:"wait,omitempty"` // pointer: nil = default (true)
	Variables        map[string]string `json:"variables,omitempty"`
	ArtifactsSend    []string          `json:"artifacts_send,omitempty"`
	ArtifactsReceive []string          `json:"artifacts_receive,omitempty"`
	Artifacts        struct {
		Upload   []jsonArtifactUpload   `json:"upload"`
		Download []jsonArtifactDownload `json:"download"`
	} `json:"artifacts"`
}

type jsonArtifactUpload struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type jsonArtifactDownload struct {
	Name string `json:"name"`
	Dest string `json:"dest"`
}

// Compile reads a pipeline file (JSON or YAML) and returns the canonical IR.
func Compile(path string) (*pipeline.Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline file %s: %w", path, err)
	}

	// Convert YAML to JSON when the extension is .yml or .yaml.
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yml" || ext == ".yaml" {
		data, err = yamlToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("parsing YAML pipeline %s: %w", path, err)
		}
	}

	var jp jsonPipeline
	if err := json.Unmarshal(data, &jp); err != nil {
		return nil, fmt.Errorf("parsing pipeline: %w", err)
	}

	if jp.Name == "" {
		return nil, fmt.Errorf("pipeline must have a 'name' field")
	}
	if len(jp.Steps) == 0 {
		return nil, fmt.Errorf("pipeline must have at least one step")
	}

	steps := make([]*pipeline.Step, 0, len(jp.Steps))
	for i, js := range jp.Steps {
		step, err := compileStep(js, i)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", js.ID, err)
		}
		steps = append(steps, step)
	}

	return &pipeline.Pipeline{
		Name:  jp.Name,
		Steps: steps,
	}, nil
}

func compileStep(js jsonStep, index int) (*pipeline.Step, error) {
	id := js.ID
	if id == "" {
		id = fmt.Sprintf("step-%d", index+1)
	}

	name := js.Name
	if name == "" {
		name = id
	}

	image := js.Image
	// image validation deferred until after stepType is determined

	var command []string
	if js.Script != "" {
		// External script file — interpreter inferred from extension.
		// Path is resolved to /workspace/<path> inside the container.
		interp := scriptInterpreter(js.Script)
		scriptPath := "/workspace/" + strings.TrimLeft(js.Script, "/")
		command = []string{interp, scriptPath}
	} else if js.Run != "" {
		command = []string{"sh", "-c", js.Run}
	} else if len(js.Command) > 0 {
		command = js.Command
	}
	// command may be nil for pipeline steps — validated later

	workdir := js.WorkDir
	if workdir == "" {
		workdir = "/workspace"
	}

	var timeout time.Duration
	if js.Timeout != "" {
		var err error
		timeout, err = time.ParseDuration(js.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", js.Timeout, err)
		}
	}

	stepType := js.Type
	if stepType == "" {
		stepType = "task"
	}

	if image == "" && stepType != "pipeline" {
		return nil, fmt.Errorf("image is required")
	}

	// Parse artifact upload declarations
	// A plain string "dist/myapp" is treated as {path: "dist/myapp"}
	var uploads []pipeline.ArtifactUploadSpec
	for _, u := range js.Artifacts.Upload {
		name := u.Name
		if name == "" {
			// Default name = last path component without leading wildcards
			parts := strings.Split(u.Path, "/")
			name = strings.TrimLeft(parts[len(parts)-1], "*")
		}

		uploads = append(uploads, pipeline.ArtifactUploadSpec{Path: u.Path, Name: name})
	}

	var downloads []pipeline.ArtifactDownloadSpec
	for _, d := range js.Artifacts.Download {
		downloads = append(downloads, pipeline.ArtifactDownloadSpec{Name: d.Name, Dest: d.Dest})
	}

	// For pipeline steps, build a PipelineRef instead of a command.
	var pipelineRef *pipeline.PipelineRef
	if stepType == "pipeline" {
		wait := true // default: block until child completes
		if js.Wait != nil {
			wait = *js.Wait
		}
		pipelineRef = &pipeline.PipelineRef{
			Path:             js.Pipeline,
			Wait:             wait,
			Variables:        js.Variables,
			ArtifactsSend:    js.ArtifactsSend,
			ArtifactsReceive: js.ArtifactsReceive,
		}
		// Pipeline steps don't need an image or command.
		image = "_pipeline_" // sentinel — never used to pull an image
		command = nil
	} else if len(command) == 0 {
		return nil, fmt.Errorf("either 'run', 'command', or 'pipeline' is required")
	}

	return &pipeline.Step{
		ID:                id,
		Name:              name,
		Image:             image,
		Command:           command,
		WorkDir:           workdir,
		Env:               js.Env,
		DependsOn:         js.DependsOn,
		Inputs:            js.Inputs,
		Timeout:           timeout,
		Secrets:           js.Secrets,
		Type:              stepType,
		DockerSocket:      js.DockerSocket,
		ArtifactUploads:   uploads,
		ArtifactDownloads: downloads,
		PipelineRef:       pipelineRef,
	}, nil
}
