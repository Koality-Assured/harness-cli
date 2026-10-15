package chat

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// PromptChoice presents a multiple choice prompt to the user and returns their selection.
// In non-interactive mode (reader == nil), it returns the first or recommended option immediately.
func PromptChoice(question string, options []string, isMultiSelect bool, reader io.Reader, writer io.Writer) ([]string, error) {
	// Non-interactive mode: return recommended option or first option immediately without reading.
	if reader == nil {
		if len(options) == 0 {
			return []string{}, nil
		}
		for _, opt := range options {
			lower := strings.ToLower(opt)
			if strings.Contains(lower, "(recommended)") || strings.Contains(lower, "recommended") {
				return []string{opt}, nil
			}
		}
		return []string{options[0]}, nil
	}

	if writer == nil {
		writer = io.Discard
	}

	scanner := bufio.NewScanner(reader)

	for {
		if strings.TrimSpace(question) != "" {
			fmt.Fprintln(writer, question)
		}
		for i, opt := range options {
			fmt.Fprintf(writer, "  [%d] %s\n", i+1, opt)
		}
		fmt.Fprintln(writer, "  [c] Custom answer")

		if isMultiSelect {
			fmt.Fprint(writer, "Select option(s) (comma-separated numbers or [c] for custom): ")
		} else {
			fmt.Fprintf(writer, "Select an option ([1-%d] or [c]): ", len(options))
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Check for custom answer entry
		if strings.EqualFold(line, "c") || strings.EqualFold(line, "custom") {
			fmt.Fprint(writer, "Enter custom answer: ")
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return nil, err
				}
				return nil, io.EOF
			}
			custom := strings.TrimSpace(scanner.Text())
			return []string{custom}, nil
		}
		if strings.HasPrefix(strings.ToLower(line), "c ") {
			custom := strings.TrimSpace(line[2:])
			return []string{custom}, nil
		}

		if isMultiSelect {
			tokens := strings.FieldsFunc(line, func(r rune) bool {
				return r == ',' || r == ' ' || r == '\t'
			})
			var selected []string
			seen := make(map[string]bool)
			valid := true
			for _, tok := range tokens {
				idx, err := strconv.Atoi(tok)
				if err != nil || idx < 1 || idx > len(options) {
					valid = false
					break
				}
				opt := options[idx-1]
				if !seen[opt] {
					seen[opt] = true
					selected = append(selected, opt)
				}
			}
			if valid && len(selected) > 0 {
				return selected, nil
			}
		} else {
			idx, err := strconv.Atoi(line)
			if err == nil && idx >= 1 && idx <= len(options) {
				return []string{options[idx-1]}, nil
			}
			// Allow exact string match of an option
			for _, opt := range options {
				if strings.EqualFold(line, opt) {
					return []string{opt}, nil
				}
			}
		}

		fmt.Fprintln(writer, "Invalid selection. Please try again.")
	}
}

// HandleAskQuestion processes arguments for the ask_question tool, formats the
// terminal prompt, and returns the user's answers in a structured result map.
func HandleAskQuestion(args map[string]any, isInteractive bool, reader io.Reader, writer io.Writer) (map[string]any, error) {
	if args == nil {
		return map[string]any{"answers": []string{}, "selected": []string{}}, nil
	}

	// Check if batch questions array was supplied
	if rawQuestions, ok := args["questions"].([]any); ok && len(rawQuestions) > 0 {
		var allAnswers []string
		for _, qItem := range rawQuestions {
			qMap, ok := qItem.(map[string]any)
			if !ok {
				continue
			}
			res, err := HandleAskQuestion(qMap, isInteractive, reader, writer)
			if err != nil {
				return nil, err
			}
			if ans, ok := res["answers"].([]string); ok {
				allAnswers = append(allAnswers, ans...)
			}
		}
		result := map[string]any{
			"answers":  allAnswers,
			"selected": allAnswers,
		}
		if len(allAnswers) > 0 {
			result["answer"] = allAnswers[0]
		}
		return result, nil
	}

	question, _ := args["question"].(string)

	var options []string
	if rawOpts, ok := args["options"].([]string); ok {
		options = rawOpts
	} else if rawOpts, ok := args["options"].([]any); ok {
		for _, item := range rawOpts {
			if s, ok := item.(string); ok {
				options = append(options, s)
			} else if item != nil {
				options = append(options, fmt.Sprint(item))
			}
		}
	}

	isMultiSelect := false
	if v, ok := args["is_multi_select"].(bool); ok {
		isMultiSelect = v
	} else if v, ok := args["isMultiSelect"].(bool); ok {
		isMultiSelect = v
	}

	var r io.Reader = reader
	if !isInteractive {
		r = nil
	}

	answers, err := PromptChoice(question, options, isMultiSelect, r, writer)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"answers":  answers,
		"selected": answers,
	}
	if len(answers) > 0 {
		result["answer"] = answers[0]
	}
	return result, nil
}

// AskQuestionToolDefinition returns the ToolDefinition for the ask_question tool.
func AskQuestionToolDefinition(isInteractive bool, in io.Reader, out io.Writer) ToolDefinition {
	return ToolDefinition{
		Name:        "ask_question",
		Description: "Prompt the user with an interactive multiple-choice question or custom response",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask the user",
				},
				"options": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "List of choices for the user to select from",
				},
				"is_multi_select": map[string]any{
					"type":        "boolean",
					"description": "Whether multiple choices can be selected",
				},
				"questions": map[string]any{
					"type":        "array",
					"description": "Optional batch of questions to ask the user",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"question": map[string]any{"type": "string"},
							"options": map[string]any{
								"type":  "array",
								"items": map[string]any{"type": "string"},
							},
							"is_multi_select": map[string]any{"type": "boolean"},
						},
					},
				},
			},
			"additionalProperties": true,
		},
		Handler: func(args map[string]any) (any, error) {
			return HandleAskQuestion(args, isInteractive, in, out)
		},
	}
}

// RegisterAskQuestionTool registers ask_question into the provided ToolRegistry.
func RegisterAskQuestionTool(registry *ToolRegistry, isInteractive bool, in io.Reader, out io.Writer) error {
	def := AskQuestionToolDefinition(isInteractive, in, out)
	if registry != nil && registry.tools != nil {
		if _, exists := registry.tools[def.Name]; exists {
			registry.tools[def.Name] = def
			return nil
		}
	}
	return registry.Register(def.Name, def.Description, def.InputSchema, def.Handler)
}
