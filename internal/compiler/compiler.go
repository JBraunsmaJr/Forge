// Package compiler translates a pipeline definition file (JSON or YAML) into Forge's canonical pipeline IR
package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/JBraunsmaJr/forge/internal/pipeline"
)

var imageRegex = regexp.MustCompile(`^[a-zA-Z0-9\.\-\/]+(:[a-zA-Z0-9\.\-]+)?$`)

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
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Image            string              `json:"image"`
	Entrypoint       []string            `json:"entrypoint,omitempty"`
	Run              string              `json:"run"`
	Command          []string            `json:"command"`
	WorkDir          string              `json:"workdir"`
	Env              map[string]string   `json:"env"`
	DependsOn        []string            `json:"depends_on"`
	Inputs           []string            `json:"inputs"`
	Timeout          string              `json:"timeout"`
	Secrets          []string            `json:"secrets"`
	Type             string              `json:"type"` // "task" (default) | "generator"
	Uses             string              `json:"uses,omitempty"`
	Condition        string              `json:"condition"`
	AlwaysRun        bool                `json:"always_run"`
	Matrix           map[string][]string `json:"matrix,omitempty"`
	Release          jsonRelease         `json:"release,omitempty"`
	DockerSocket     bool                `json:"docker_socket"`
	Script           string              `json:"script,omitempty"`
	Pipeline         string              `json:"pipeline,omitempty"`
	Wait             *bool               `json:"wait,omitempty"` // pointer: nil = default (true)
	Variables        map[string]string   `json:"variables,omitempty"`
	ArtifactsSend    []string            `json:"artifacts_send,omitempty"`
	ArtifactsReceive []string            `json:"artifacts_receive,omitempty"`
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

type jsonRelease struct {
	Name      string   `json:"name"`
	Tag       string   `json:"tag"`
	Body      string   `json:"body"`
	Artifacts []string `json:"artifacts"`
}

// Compile reads a pipeline file (JSON or YAML) and returns the canonical IR.
func Compile(path string) (*pipeline.Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pipeline file %s: %w", path, err)
	}
	return CompileData(data, path)
}

// CompileData parses pipeline data (JSON or YAML) and returns the canonical IR.
// The filename is used to determine the format (via extension).
func CompileData(data []byte, filename string) (*pipeline.Pipeline, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".yml" || ext == ".yaml" {
		var err error
		data, err = yamlToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("parsing YAML pipeline %s: %w", filename, err)
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
	expandedMap := make(map[string][]string)

	for i, js := range jp.Steps {
		originalID := js.ID
		if js.Uses != "" {
			resolved, err := resolveUses(js, filename)
			if err != nil {
				return nil, fmt.Errorf("step %q uses %q: %w", js.ID, js.Uses, err)
			}
			js = resolved
		}

		if len(js.Matrix) > 0 {
			matrixSteps, err := expandMatrixStep(js, i)
			if err != nil {
				return nil, fmt.Errorf("step %q matrix: %w", js.ID, err)
			}
			for _, ms := range matrixSteps {
				expandedMap[originalID] = append(expandedMap[originalID], ms.ID)
			}
			steps = append(steps, matrixSteps...)
			continue
		}

		step, err := compileStep(js, i)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", js.ID, err)
		}
		steps = append(steps, step)
		expandedMap[originalID] = []string{step.ID}
	}

	// Post-process dependencies to handle matrix expansion
	for _, step := range steps {
		var newDeps []string
		for _, dep := range step.DependsOn {
			if expanded, ok := expandedMap[dep]; ok {
				newDeps = append(newDeps, expanded...)
			} else {
				// Keep it as is, might be a non-existent step which p.Validate() will catch
				newDeps = append(newDeps, dep)
			}
		}
		step.DependsOn = newDeps
	}

	p := &pipeline.Pipeline{
		Name:  jp.Name,
		Steps: steps,
	}

	if err := p.Validate(); err != nil {
		return nil, err
	}

	return p, nil
}

func resolveUses(js jsonStep, currentFile string) (jsonStep, error) {
	templatePath := js.Uses
	if !filepath.IsAbs(templatePath) {
		templatePath = filepath.Join(filepath.Dir(currentFile), templatePath)
	}

	data, err := os.ReadFile(templatePath)
	if err != nil {
		return js, fmt.Errorf("reading template %s: %w", templatePath, err)
	}

	ext := strings.ToLower(filepath.Ext(templatePath))
	if ext == ".yml" || ext == ".yaml" {
		data, err = yamlToJSON(data)
		if err != nil {
			return js, fmt.Errorf("parsing template YAML %s: %w", templatePath, err)
		}
	}

	var template jsStepTemplate
	if err := json.Unmarshal(data, &template); err != nil {
		/*
			Template might be a full pipeline fil, or just a step definition.
			If it's a full pipeline we don't support using it as a step template yet
		*/
		return js, fmt.Errorf("parsing template: %w", err)
	}

	// Merge template into js. Current js fields (if non-zero) win.
	if js.ID == "" {
		js.ID = template.ID
	}
	if js.Name == "" {
		js.Name = template.Name
	}
	if js.Image == "" {
		js.Image = template.Image
	}
	if len(js.Entrypoint) == 0 {
		js.Entrypoint = template.Entrypoint
	}
	if js.Run == "" {
		js.Run = template.Run
	}
	if len(js.Command) == 0 {
		js.Command = template.Command
	}
	if js.WorkDir == "" {
		js.WorkDir = template.WorkDir
	}
	if js.Timeout == "" {
		js.Timeout = template.Timeout
	}
	if js.Type == "" {
		js.Type = template.Type
	}
	if js.Condition == "" {
		js.Condition = template.Condition
	}
	if !js.AlwaysRun && template.AlwaysRun {
		js.AlwaysRun = template.AlwaysRun
	}
	if !js.DockerSocket && template.DockerSocket {
		js.DockerSocket = template.DockerSocket
	}
	if js.Release.Tag == "" {
		js.Release = template.Release
	}
	if js.Script == "" {
		js.Script = template.Script
	}
	if js.Pipeline == "" {
		js.Pipeline = template.Pipeline
	}
	if js.Wait == nil {
		js.Wait = template.Wait
	}

	// Merge maps/slices
	if js.Env == nil {
		js.Env = make(map[string]string)
	}
	for k, v := range template.Env {
		if _, ok := js.Env[k]; !ok {
			js.Env[k] = v
		}
	}

	if js.Variables == nil {
		js.Variables = make(map[string]string)
	}
	for k, v := range template.Variables {
		if _, ok := js.Variables[k]; !ok {
			js.Variables[k] = v
		}
	}

	if len(js.DependsOn) == 0 {
		js.DependsOn = template.DependsOn
	}
	if len(js.Inputs) == 0 {
		js.Inputs = template.Inputs
	}
	if len(js.Secrets) == 0 {
		js.Secrets = template.Secrets
	}
	if len(js.ArtifactsSend) == 0 {
		js.ArtifactsSend = template.ArtifactsSend
	}
	if len(js.ArtifactsReceive) == 0 {
		js.ArtifactsReceive = template.ArtifactsReceive
	}

	if len(js.Artifacts.Upload) == 0 {
		js.Artifacts.Upload = template.Artifacts.Upload
	}
	if len(js.Artifacts.Download) == 0 {
		js.Artifacts.Download = template.Artifacts.Download
	}

	return js, nil
}

// jsStepTemplate is a helper for unmarshaling a step template.
// It's basically jsonStep without Uses to avoid recursion.
type jsStepTemplate struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	Entrypoint       []string          `json:"entrypoint,omitempty"`
	Run              string            `json:"run"`
	Command          []string          `json:"command"`
	WorkDir          string            `json:"workdir"`
	Env              map[string]string `json:"env"`
	DependsOn        []string          `json:"depends_on"`
	Inputs           []string          `json:"inputs"`
	Timeout          string            `json:"timeout"`
	Secrets          []string          `json:"secrets"`
	Type             string            `json:"type"`
	Condition        string            `json:"condition"`
	AlwaysRun        bool              `json:"always_run"`
	DockerSocket     bool              `json:"docker_socket"`
	Release          jsonRelease       `json:"release,omitempty"`
	Script           string            `json:"script,omitempty"`
	Pipeline         string            `json:"pipeline,omitempty"`
	Wait             *bool             `json:"wait,omitempty"`
	Variables        map[string]string `json:"variables,omitempty"`
	ArtifactsSend    []string          `json:"artifacts_send,omitempty"`
	ArtifactsReceive []string          `json:"artifacts_receive,omitempty"`
	Artifacts        struct {
		Upload   []jsonArtifactUpload   `json:"upload"`
		Download []jsonArtifactDownload `json:"download"`
	} `json:"artifacts"`
}

func expandMatrixStep(js jsonStep, baseIndex int) ([]*pipeline.Step, error) {
	keys := make([]string, 0, len(js.Matrix))
	for k := range js.Matrix {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	combinations := []map[string]string{{}}
	for _, key := range keys {
		values := js.Matrix[key]
		var next []map[string]string
		for _, combo := range combinations {
			for _, val := range values {
				newCombo := make(map[string]string)
				for k, v := range combo {
					newCombo[k] = v
				}
				newCombo[key] = val
				next = append(next, newCombo)
			}
		}
		combinations = next
	}

	steps := make([]*pipeline.Step, 0, len(combinations))
	for i, combo := range combinations {
		// Clone the step and inject variables
		cloned := js
		cloned.Matrix = nil // remove matrix from clones
		if cloned.Env == nil {
			cloned.Env = make(map[string]string)
		}

		// Build ID and Name suffix
		suffix := ""
		for _, key := range keys {
			val := combo[key]
			cloned.Env[key] = val
			suffix += fmt.Sprintf("-%s", val)
		}

		if cloned.ID != "" {
			cloned.ID += suffix
		}
		if cloned.Name != "" {
			cloned.Name += " (" + strings.TrimPrefix(suffix, "-") + ")"
		}

		// Interpolate variables in string fields
		replacer := func(s string) string {
			for k, v := range combo {
				s = strings.ReplaceAll(s, "${{ matrix."+k+" }}", v)
			}
			return s
		}

		cloned.Image = replacer(cloned.Image)
		cloned.Run = replacer(cloned.Run)
		for j, v := range cloned.Command {
			cloned.Command[j] = replacer(v)
		}
		for j, v := range cloned.Entrypoint {
			cloned.Entrypoint[j] = replacer(v)
		}

		for j, u := range cloned.Artifacts.Upload {
			cloned.Artifacts.Upload[j].Path = replacer(u.Path)
			cloned.Artifacts.Upload[j].Name = replacer(u.Name)
		}
		for j, d := range cloned.Artifacts.Download {
			cloned.Artifacts.Download[j].Name = replacer(d.Name)
			cloned.Artifacts.Download[j].Dest = replacer(d.Dest)
		}

		cloned.Release.Name = replacer(cloned.Release.Name)
		cloned.Release.Tag = replacer(cloned.Release.Tag)
		cloned.Release.Body = replacer(cloned.Release.Body)
		for j, v := range cloned.Release.Artifacts {
			cloned.Release.Artifacts[j] = replacer(v)
		}

		step, err := compileStep(cloned, baseIndex+i)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}

	return steps, nil
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

	// image validation deferred until after stepType is determined.
	var command []string
	if js.Script != "" {
		// External script file - interpreter inferred from extension. Path is relative to workspace in the container.
		interp := scriptInterpreter(js.Script)
		scriptPath := "/workspace/" + strings.TrimLeft(js.Script, "/")
		command = []string{interp, scriptPath}
	} else if js.Run != "" {
		command = []string{"sh", "-c", "set -e\n" + js.Run}
	} else if len(js.Command) > 0 {
		command = js.Command
	}
	// command may be nil for pipeline steps. This is validated later.
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

	if image == "" && stepType != "pipeline" && stepType != "approval" && stepType != "release" {
		return nil, fmt.Errorf("image is required")
	}

	if image != "" && !imageRegex.MatchString(image) {
		return nil, fmt.Errorf("invalid image name: %q", image)
	}

	// Parse artifact upload declarations
	// A plain string "dist/myapp" is treated as {path: "dist/myapp"}
	var uploads []pipeline.ArtifactUploadSpec
	for _, u := range js.Artifacts.Upload {
		name := u.Name
		if name == "" {

			parts := strings.Split(u.Path, "/")
			name = strings.TrimLeft(parts[len(parts)-1], "*")
		}

		uploads = append(uploads, pipeline.ArtifactUploadSpec{Path: u.Path, Name: name})
	}

	var downloads []pipeline.ArtifactDownloadSpec
	for _, d := range js.Artifacts.Download {
		downloads = append(downloads, pipeline.ArtifactDownloadSpec{Name: d.Name, Dest: d.Dest})
	}

	var pipelineRef *pipeline.PipelineRef
	var release *pipeline.ReleaseConfig

	if stepType == "pipeline" {
		wait := true // default: block until child completes.
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
		image = "_pipeline_" // sentinel - never used to pull an image.
		command = nil
	} else if stepType == "release" {
		if js.Release.Tag == "" {
			return nil, fmt.Errorf("release step requires at least a 'tag'")
		}
		release = &pipeline.ReleaseConfig{
			Name:      js.Release.Name,
			Tag:       js.Release.Tag,
			Body:      js.Release.Body,
			Artifacts: js.Release.Artifacts,
		}
		image = "_release_" // sentinel
		command = nil
	} else if len(command) == 0 && stepType != "approval" && stepType != "release" {
		return nil, fmt.Errorf("either 'run', 'command', or 'pipeline' is required")
	}

	return &pipeline.Step{
		ID:                id,
		Name:              name,
		Image:             image,
		Entrypoint:        js.Entrypoint,
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
		Condition:         js.Condition,
		AlwaysRun:         js.AlwaysRun,
		Release:           release,
	}, nil
}
