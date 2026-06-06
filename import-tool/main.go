package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFiles embed.FS

const (
	appVersion             = "2026-06-06-0045"
	fieldParentLink        = "fldSyjvmzA"
	fieldPromptIndex       = "fldW6rO2LU"
	fieldPrompt            = "fldBpE9COv"
	fieldSessionID         = "fldaMDOOJL"
	fieldRolloutID         = "fldqgS0GPQ"
	fieldScore             = "fldvFVIm4O"
	fieldScoreReason       = "fld7hrms66"
	fieldGitDiff           = "fld3Jhw2G1"
	fieldModelName         = "fldPxbX1x9"
	fieldCategory          = "fldN5I3M6K"
	fieldDifficulty        = "fldFNZopN2"
	fieldTechStack         = "fldw4LTPb2"
	fieldModuleTags        = "fld8eplq46"
	fieldTaskCount         = "fldxO6aLVP"
	rolloutsPerPrompt      = 5
	legacyPromptFieldRunes = 1000
)

var (
	promptFilePattern      = regexp.MustCompile(`(?i)^prompt-(\d+)\.txt$`)
	rolloutFilePattern     = regexp.MustCompile(`^(\d+)\.(id|score|patch|log)$`)
	configInitURLPattern   = regexp.MustCompile(`https://open\.feishu\.cn/\S+`)
	configInitStateMu      sync.Mutex
	configInitStateCurrent configInitState
)

var modelOrder = []string{
	"Doubao-Seed-2.0-Code",
	"GPT5.4",
	"Gemini 3.1 pro",
	"DeepSeek-v4",
}

type configInitState struct {
	Running         bool
	Done            bool
	Configured      bool
	VerificationURL string
	QRASCII         string
	QRImageURL      string
	LastMessage     string
	Error           string
	StartedAt       time.Time
	Cmd             *exec.Cmd
}

type FieldOption struct {
	Name string `json:"name"`
}

type RolloutMapping struct {
	Index          int      `json:"index"`
	RolloutID      string   `json:"rolloutId"`
	PromptIndex    int      `json:"prompt"`
	ModelName      string   `json:"modelName"`
	Score          string   `json:"score"`
	Reason         string   `json:"reason"`
	SessionID      string   `json:"sessionId"`
	PatchPath      string   `json:"patchPath"`
	PromptRecordID string   `json:"promptRecordId"`
	RecordID       string   `json:"recordId"`
	Attachments    []string `json:"attachments"`
	SourceDir      string   `json:"sourceDir"`
	PromptPath     string   `json:"promptPath"`
	PromptText     string   `json:"promptText"`
}

type ImportResult struct {
	Index    int    `json:"index"`
	RecordID string `json:"recordId"`
	Success  bool   `json:"success"`
	Message  string `json:"message"`
}

type ImportWarning struct {
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

type ScanIssue struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ScanReport struct {
	FolderPath       string      `json:"folderPath"`
	DatasetCount     int         `json:"datasetCount"`
	PromptFileCount  int         `json:"promptFileCount"`
	RolloutCount     int         `json:"rolloutCount"`
	IgnoredFileCount int         `json:"ignoredFileCount"`
	Issues           []ScanIssue `json:"issues"`
}

type PromptFile struct {
	GroupPath  string
	Index      int
	Path       string
	Content    string
	TaskType   string
	Category   string
	Difficulty string
	ModuleTags string
	TechStack  string
}

type RolloutBundle struct {
	GroupPath   string
	Index       int
	RolloutID   string
	PromptIndex int
	IDPath      string
	ScorePath   string
	PatchPath   string
	LogPath     string
	SessionID   string
	Score       string
	Reason      string
	ModelName   string
	ModelRaw    string
}

type ExistingRecord struct {
	RecordID    string
	PromptIndex int
	PromptText  string
	RolloutID   string
	SessionID   string
}

type RecordSnapshot struct {
	RecordID string
	Values   map[string]string
}

type TableField struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Options []FieldOption `json:"options"`
}

func resolveCommandPath(name string) string {
	if name != "lark-cli" {
		return name
	}

	candidates := []string{"lark-cli"}
	if runtime.GOOS == "windows" {
		candidates = []string{"lark-cli.exe", "lark-cli.cmd", "lark-cli.bat", "lark-cli"}
	}

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		for _, candidate := range candidates {
			fullPath := filepath.Join(exeDir, candidate)
			if info, statErr := os.Stat(fullPath); statErr == nil && !info.IsDir() {
				return fullPath
			}
		}
	}

	for _, candidate := range candidates {
		if fullPath, err := exec.LookPath(candidate); err == nil {
			return fullPath
		}
	}

	return name
}

func newCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(resolveCommandPath(name), args...)
}

func runCmd(name string, args ...string) ([]byte, error) {
	return runCmdInDir("", name, args...)
}

func runCmdInDir(dir, name string, args ...string) ([]byte, error) {
	cmd := newCommand(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	return cmd.CombinedOutput()
}

func generateQRCodePayload(verificationURL string) (string, string) {
	larkCLIPath := resolveCommandPath("lark-cli")
	lowerPath := strings.ToLower(larkCLIPath)

	runQRCode := func(dir string, extraArgs ...string) ([]byte, error) {
		if runtime.GOOS == "windows" && (strings.HasSuffix(lowerPath, ".cmd") || strings.HasSuffix(lowerPath, ".bat")) {
			wrapperDir := filepath.Dir(larkCLIPath)
			nodePath := filepath.Join(wrapperDir, "node.exe")
			if _, err := os.Stat(nodePath); err != nil {
				nodePath = "node"
			}
			scriptPath := filepath.Join(wrapperDir, "node_modules", "@larksuite", "cli", "scripts", "run.js")
			args := append([]string{scriptPath, "auth", "qrcode", verificationURL}, extraArgs...)
			cmd := exec.Command(nodePath, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
				"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
			)
			return cmd.CombinedOutput()
		}

		cmd := newCommand("lark-cli", append([]string{"auth", "qrcode", verificationURL}, extraArgs...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
			"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
		)
		return cmd.CombinedOutput()
	}

	asciiOut, _ := runQRCode("", "--ascii")
	ascii := string(asciiOut)

	tempDir, err := os.MkdirTemp("", "import-tool-qr-*")
	if err != nil {
		return ascii, ""
	}
	defer os.RemoveAll(tempDir)

	if out, err := runQRCode(tempDir, "--output", "qr.png"); err != nil {
		_ = out
		return ascii, ""
	}

	pngBytes, err := os.ReadFile(filepath.Join(tempDir, "qr.png"))
	if err != nil || len(pngBytes) == 0 {
		return ascii, ""
	}
	return ascii, "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
}

func resetConfigInitStateLocked() {
	configInitStateCurrent = configInitState{}
}

func stopConfigInitFlow() {
	configInitStateMu.Lock()
	cmd := configInitStateCurrent.Cmd
	resetConfigInitStateLocked()
	configInitStateMu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func updateConfigInitFromLine(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}

	var shouldLoadQR bool
	var verificationURL string

	configInitStateMu.Lock()
	configInitStateCurrent.LastMessage = trimmed
	if configInitStateCurrent.VerificationURL == "" {
		if match := configInitURLPattern.FindString(trimmed); match != "" {
			configInitStateCurrent.VerificationURL = match
			verificationURL = match
			shouldLoadQR = true
		}
	}
	configInitStateMu.Unlock()

	if shouldLoadQR {
		qrASCII, qrImageURL := generateQRCodePayload(verificationURL)
		configInitStateMu.Lock()
		if configInitStateCurrent.VerificationURL == verificationURL {
			if configInitStateCurrent.QRASCII == "" {
				configInitStateCurrent.QRASCII = qrASCII
			}
			if configInitStateCurrent.QRImageURL == "" {
				configInitStateCurrent.QRImageURL = qrImageURL
			}
		}
		configInitStateMu.Unlock()
	}
}

func readConfigInitPipe(reader io.ReadCloser) {
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		updateConfigInitFromLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		configInitStateMu.Lock()
		if configInitStateCurrent.Error == "" {
			configInitStateCurrent.Error = fmt.Sprintf("读取飞书配置输出失败: %v", err)
		}
		configInitStateMu.Unlock()
	}
}

func finishConfigInitFlow(runCmd *exec.Cmd, waitErr error) {
	configured, _, statusErr := getConfigStatus()

	configInitStateMu.Lock()
	defer configInitStateMu.Unlock()
	if configInitStateCurrent.Cmd != runCmd {
		return
	}

	configInitStateCurrent.Running = false
	configInitStateCurrent.Done = true
	configInitStateCurrent.Cmd = nil
	if statusErr == nil && configured {
		configInitStateCurrent.Configured = true
		configInitStateCurrent.Error = ""
		return
	}
	if configInitStateCurrent.Error != "" {
		return
	}
	if waitErr != nil {
		message := strings.TrimSpace(configInitStateCurrent.LastMessage)
		if message == "" {
			message = fmt.Sprintf("飞书应用配置未完成: %v", waitErr)
		}
		configInitStateCurrent.Error = message
		return
	}
	configInitStateCurrent.Error = "请先在飞书页面完成应用创建或绑定，然后再点击“我已完成配置”"
}

func waitConfigInitReady(timeout time.Duration) (configInitState, error) {
	deadline := time.Now().Add(timeout)
	for {
		configInitStateMu.Lock()
		state := configInitStateCurrent
		configInitStateMu.Unlock()

		if state.VerificationURL != "" {
			if strings.TrimSpace(state.QRASCII) != "" || time.Since(state.StartedAt) > 2*time.Second {
				return state, nil
			}
		}
		if state.Error != "" && state.Done {
			return state, fmt.Errorf(state.Error)
		}
		if time.Now().After(deadline) {
			return state, fmt.Errorf("正在生成飞书配置链接，请稍后重试")
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func startConfigInitFlow() (configInitState, error) {
	configInitStateMu.Lock()
	if configInitStateCurrent.Running {
		configInitStateMu.Unlock()
		return waitConfigInitReady(12 * time.Second)
	}
	if configInitStateCurrent.Configured {
		state := configInitStateCurrent
		configInitStateMu.Unlock()
		return state, nil
	}

	resetConfigInitStateLocked()
	cmd := newCommand("lark-cli", "config", "init", "--new", "--lang", "zh_cn", "--force-init")
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		configInitStateMu.Unlock()
		return configInitState{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		configInitStateMu.Unlock()
		return configInitState{}, err
	}
	if err := cmd.Start(); err != nil {
		configInitStateMu.Unlock()
		return configInitState{}, err
	}

	configInitStateCurrent = configInitState{
		Running:     true,
		StartedAt:   time.Now(),
		Cmd:         cmd,
		LastMessage: "正在生成飞书配置链接...",
	}
	configInitStateMu.Unlock()

	go readConfigInitPipe(stdout)
	go readConfigInitPipe(stderr)
	go func(runCmd *exec.Cmd) {
		finishConfigInitFlow(runCmd, runCmd.Wait())
	}(cmd)

	return waitConfigInitReady(12 * time.Second)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("X-Import-Tool-Version", appVersion)
	_ = json.NewEncoder(w).Encode(data)
}

func appendImportDebugLog(payload map[string]interface{}) {
	payload["version"] = appVersion
	payload["time"] = time.Now().Format(time.RFC3339)
	line, err := json.Marshal(payload)
	if err != nil {
		return
	}
	logPath := filepath.Join(os.TempDir(), "import-tool-debug.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func withNoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.Header().Set("X-Import-Tool-Version", appVersion)
		next.ServeHTTP(w, r)
	})
}

func jsonEnvelope(out []byte) ([]byte, error) {
	jsonStart := bytes.Index(out, []byte("{"))
	if jsonStart == -1 {
		return nil, fmt.Errorf("未找到 JSON 响应")
	}
	return out[jsonStart:], nil
}

func jsonObjectEnvelope(out []byte) ([]byte, error) {
	jsonStart := bytes.Index(out, []byte("{"))
	jsonEnd := bytes.LastIndex(out, []byte("}"))
	if jsonStart == -1 || jsonEnd == -1 || jsonEnd < jsonStart {
		return nil, fmt.Errorf("未找到 JSON 响应")
	}
	return out[jsonStart : jsonEnd+1], nil
}

func canonicalPath(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

func normalizeText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func promptFieldValue(prompt string) string {
	return normalizeText(prompt)
}

func legacyPromptFieldValue(prompt string) string {
	normalized := normalizeText(prompt)
	runes := []rune(normalized)
	if len(runes) <= legacyPromptFieldRunes {
		return normalized
	}
	return strings.TrimSpace(string(runes[:legacyPromptFieldRunes]))
}

func pathLabel(rootPath, currentPath string) string {
	rel, err := filepath.Rel(rootPath, currentPath)
	if err != nil {
		return currentPath
	}
	if rel == "." {
		return filepath.Base(currentPath)
	}
	return rel
}

func promptIdentityKey(index int, prompt string) string {
	return strconv.Itoa(index) + "|" + promptFieldValue(prompt)
}

func promptIdentityCandidates(index int, prompt string) []string {
	fullKey := promptIdentityKey(index, prompt)
	legacyKey := strconv.Itoa(index) + "|" + legacyPromptFieldValue(prompt)
	if legacyKey == fullKey {
		return []string{fullKey}
	}
	return []string{fullKey, legacyKey}
}

func findExistingPromptRecord(existingPromptMap map[string]ExistingRecord, prompt PromptFile) (ExistingRecord, bool) {
	for _, key := range promptIdentityCandidates(prompt.Index, prompt.Content) {
		if existing, ok := existingPromptMap[key]; ok {
			return existing, true
		}
	}
	return ExistingRecord{}, false
}

func normalizeRecordID(s string) string {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.ReplaceAll(trimmed, "\u200b", "")
	trimmed = strings.ReplaceAll(trimmed, "\ufeff", "")
	if match := regexp.MustCompile(`rec[0-9A-Za-z]+`).FindString(trimmed); match != "" {
		return match
	}
	return trimmed
}

func normalizeLooseIdentifier(s string) string {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	trimmed = strings.TrimLeft(trimmed, "0")
	if trimmed == "" {
		return "0"
	}
	return trimmed
}

func identifiersMatch(input, candidate string) bool {
	left := normalizeLooseIdentifier(input)
	right := normalizeLooseIdentifier(candidate)
	if left == right {
		return true
	}
	leftNum, leftErr := strconv.Atoi(left)
	rightNum, rightErr := strconv.Atoi(right)
	return leftErr == nil && rightErr == nil && leftNum == rightNum
}

func promptGroupKey(groupPath string, promptIndex int) string {
	return canonicalPath(groupPath) + "|" + strconv.Itoa(promptIndex)
}

func rolloutIdentityKey(rolloutID, sessionID string) string {
	return strings.TrimSpace(rolloutID) + "|" + strings.TrimSpace(sessionID)
}

func extractNumberText(s string) string {
	var num strings.Builder
	for _, c := range s {
		if (c >= '0' && c <= '9') || c == '.' {
			num.WriteRune(c)
		}
	}
	return strings.Trim(num.String(), ".")
}

func splitLabelValue(line string) (string, string, bool) {
	parts := strings.FieldsFunc(line, func(r rune) bool {
		return r == ':' || r == '：' || r == '='
	})
	if len(parts) < 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(strings.Join(parts[1:], ":"))
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func isPromptHeaderLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return regexp.MustCompile(`(?i)^prompt\s*\d+\s*$`).MatchString(trimmed)
}

func isPromptMetaLine(line string) bool {
	key, _, ok := splitLabelValue(line)
	if !ok {
		return false
	}
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(key, "任务类型"), strings.Contains(key, "分类"), strings.Contains(lowerKey, "task type"), strings.Contains(lowerKey, "category"):
		return true
	case strings.Contains(key, "难度"), strings.Contains(lowerKey, "difficulty"):
		return true
	case strings.Contains(key, "任务标签"), strings.Contains(key, "模块标签"), strings.Contains(lowerKey, "module_tags"), strings.Contains(lowerKey, "module tags"):
		return true
	case strings.Contains(key, "涉及模块"), strings.Contains(key, "技术栈"), strings.Contains(key, "涉及技术栈"), strings.Contains(lowerKey, "tech_stack"), strings.Contains(lowerKey, "tech stack"):
		return true
	}
	return false
}

func isPromptValidationLine(line string) bool {
	key, _, ok := splitLabelValue(line)
	if !ok {
		return false
	}
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "验证方式") || strings.Contains(lowerKey, "validation") || strings.Contains(lowerKey, "verify")
}

func parsePromptMetadata(content string) (taskType, category, difficulty, moduleTags, techStack string) {
	for _, rawLine := range strings.Split(normalizeText(content), "\n") {
		line := strings.TrimSpace(rawLine)
		key, value, ok := splitLabelValue(line)
		if !ok {
			continue
		}
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		switch {
		case strings.Contains(key, "任务类型") || strings.Contains(lowerKey, "task type"):
			taskType = value
		case strings.Contains(key, "分类") || strings.Contains(lowerKey, "category"):
			category = value
		case strings.Contains(key, "难度") || strings.Contains(lowerKey, "difficulty"):
			difficulty = value
		case strings.Contains(key, "任务标签") || strings.Contains(key, "模块标签") || strings.Contains(lowerKey, "module_tags") || strings.Contains(lowerKey, "module tags"):
			moduleTags = value
		case strings.Contains(key, "涉及模块") || strings.Contains(key, "技术栈") || strings.Contains(key, "涉及技术栈") || strings.Contains(lowerKey, "tech_stack") || strings.Contains(lowerKey, "tech stack"):
			techStack = value
		}
	}
	return strings.TrimSpace(taskType), strings.TrimSpace(category), strings.TrimSpace(difficulty), strings.TrimSpace(moduleTags), strings.TrimSpace(techStack)
}

func cleanPromptContent(content string) string {
	lines := strings.Split(normalizeText(content), "\n")
	cleaned := make([]string, 0, len(lines))
	skipLeadingBlank := true
	headerSkipped := false
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)
		if !headerSkipped && isPromptHeaderLine(trimmed) {
			headerSkipped = true
			skipLeadingBlank = true
			continue
		}
		if isPromptMetaLine(trimmed) {
			continue
		}
		if isPromptValidationLine(trimmed) {
			break
		}
		if skipLeadingBlank && trimmed == "" {
			continue
		}
		skipLeadingBlank = false
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func parseScoreDetails(path string) (score, reason, model string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", err
	}
	content := normalizeText(string(data))
	lines := strings.Split(content, "\n")

	inReason := false
	reasonLines := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			if inReason {
				reasonLines = append(reasonLines, "")
			}
			continue
		}

		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(line, "分数:"), strings.HasPrefix(lower, "score:"), strings.HasPrefix(line, "评分:"), strings.HasPrefix(lower, "score="):
			if _, value, ok := splitLabelValue(line); ok {
				score = value
			}
			if score == "" {
				score = extractNumberText(line)
			}
			inReason = false
		case strings.HasPrefix(line, "模型:"), strings.HasPrefix(lower, "model:"), strings.HasPrefix(lower, "model="):
			if _, value, ok := splitLabelValue(line); ok {
				model = value
			}
		case strings.HasPrefix(line, "理由:"), strings.HasPrefix(lower, "reason:"), strings.HasPrefix(line, "原因:"), strings.HasPrefix(lower, "reason="):
			if _, value, ok := splitLabelValue(line); ok {
				reasonLines = append(reasonLines, value)
			}
			inReason = true
		default:
			if score == "" && (strings.Contains(line, "分数") || strings.Contains(lower, "score")) {
				score = extractNumberText(line)
				if score != "" {
					inReason = false
					continue
				}
			}
			if inReason {
				reasonLines = append(reasonLines, line)
			}
		}
	}

	if score == "" {
		score = extractNumberText(content)
	}
	if len(reasonLines) > 0 {
		reason = normalizeText(strings.Join(reasonLines, "\n"))
	}
	if reason == "" {
		reason = content
	}

	return strings.TrimSpace(score), reason, strings.TrimSpace(model), nil
}

func parseScoreFile(path string) (score, reason string, err error) {
	score, reason, _, err = parseScoreDetails(path)
	return score, reason, err
}

func resolvePromptIndex(rolloutIndex int, promptIndices []int) int {
	if len(promptIndices) == 0 {
		return 0
	}
	if rolloutIndex <= 0 {
		return promptIndices[0]
	}
	position := (rolloutIndex - 1) / rolloutsPerPrompt
	if position >= len(promptIndices) {
		position = len(promptIndices) - 1
	}
	return promptIndices[position]
}

func parseFolder(folderPath string) ([]PromptFile, []RolloutBundle, ScanReport) {
	report := ScanReport{FolderPath: strings.TrimSpace(folderPath)}
	folderPath = strings.TrimSpace(folderPath)
	if folderPath == "" {
		report.Issues = append(report.Issues, ScanIssue{Path: folderPath, Reason: "文件夹路径为空"})
		return nil, nil, report
	}

	info, err := os.Stat(folderPath)
	if err != nil {
		report.Issues = append(report.Issues, ScanIssue{Path: folderPath, Reason: fmt.Sprintf("无法访问文件夹: %v", err)})
		return nil, nil, report
	}
	if !info.IsDir() {
		report.Issues = append(report.Issues, ScanIssue{Path: folderPath, Reason: "路径不是文件夹"})
		return nil, nil, report
	}

	promptGroups := map[string]map[int]*PromptFile{}
	rolloutGroups := map[string]map[int]*RolloutBundle{}
	dataGroups := map[string]struct{}{}

	err = filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			report.Issues = append(report.Issues, ScanIssue{Path: path, Reason: walkErr.Error()})
			return nil
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		groupPath := filepath.Dir(path)

		if match := promptFilePattern.FindStringSubmatch(name); match != nil {
			report.PromptFileCount++
			index, _ := strconv.Atoi(match[1])
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				report.Issues = append(report.Issues, ScanIssue{Path: path, Reason: fmt.Sprintf("读取失败: %v", readErr)})
				return nil
			}
			rawContent := string(data)
			taskType, category, difficulty, moduleTags, techStack := parsePromptMetadata(rawContent)
			content := cleanPromptContent(rawContent)
			if promptGroups[groupPath] == nil {
				promptGroups[groupPath] = map[int]*PromptFile{}
			}
			promptGroups[groupPath][index] = &PromptFile{
				GroupPath:  groupPath,
				Index:      index,
				Path:       path,
				Content:    content,
				TaskType:   taskType,
				Category:   category,
				Difficulty: difficulty,
				ModuleTags: moduleTags,
				TechStack:  techStack,
			}
			dataGroups[groupPath] = struct{}{}
			return nil
		}

		if strings.EqualFold(name, "slot-models.json") {
			report.IgnoredFileCount++
			return nil
		}
		if strings.HasSuffix(strings.ToLower(name), ".zip") {
			report.IgnoredFileCount++
			return nil
		}

		if match := rolloutFilePattern.FindStringSubmatch(name); match != nil {
			index, _ := strconv.Atoi(match[1])
			ext := strings.ToLower(match[2])
			if rolloutGroups[groupPath] == nil {
				rolloutGroups[groupPath] = map[int]*RolloutBundle{}
			}
			bundle := rolloutGroups[groupPath][index]
			if bundle == nil {
				bundle = &RolloutBundle{
					GroupPath: groupPath,
					Index:     index,
					RolloutID: fmt.Sprintf("%02d", index),
				}
				rolloutGroups[groupPath][index] = bundle
			}
			switch ext {
			case "id":
				bundle.IDPath = path
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					report.Issues = append(report.Issues, ScanIssue{Path: path, Reason: fmt.Sprintf("读取失败: %v", readErr)})
				} else {
					bundle.SessionID = strings.TrimSpace(string(data))
				}
			case "score":
				bundle.ScorePath = path
				score, reason, model, parseErr := parseScoreDetails(path)
				if parseErr != nil {
					report.Issues = append(report.Issues, ScanIssue{Path: path, Reason: fmt.Sprintf("解析失败: %v", parseErr)})
				} else {
					bundle.Score = score
					bundle.Reason = reason
					bundle.ModelRaw = model
				}
			case "patch":
				bundle.PatchPath = path
			case "log":
				bundle.LogPath = path
				report.IgnoredFileCount++
			}
			dataGroups[groupPath] = struct{}{}
			return nil
		}

		return nil
	})
	if err != nil {
		report.Issues = append(report.Issues, ScanIssue{Path: folderPath, Reason: fmt.Sprintf("扫描失败: %v", err)})
	}

	groupPaths := make([]string, 0, len(dataGroups))
	for groupPath := range dataGroups {
		groupPaths = append(groupPaths, groupPath)
	}
	sort.Strings(groupPaths)
	report.DatasetCount = len(groupPaths)

	prompts := make([]PromptFile, 0)
	rollouts := make([]RolloutBundle, 0)

	for _, groupPath := range groupPaths {
		promptIndexMap := promptGroups[groupPath]
		rolloutIndexMap := rolloutGroups[groupPath]

		promptIndices := make([]int, 0, len(promptIndexMap))
		for index := range promptIndexMap {
			promptIndices = append(promptIndices, index)
		}
		sort.Ints(promptIndices)
		for _, index := range promptIndices {
			prompts = append(prompts, *promptIndexMap[index])
		}

		rolloutIndices := make([]int, 0, len(rolloutIndexMap))
		for index := range rolloutIndexMap {
			rolloutIndices = append(rolloutIndices, index)
		}
		sort.Ints(rolloutIndices)
		if len(rolloutIndices) > 0 && len(promptIndices) == 0 {
			report.Issues = append(report.Issues, ScanIssue{Path: groupPath, Reason: "发现三级文件，但当前目录没有 prompt-*.txt 二级数据行"})
		}
		for _, index := range rolloutIndices {
			bundle := rolloutIndexMap[index]
			if strings.TrimSpace(bundle.ModelRaw) != "" {
				bundle.ModelName = strings.TrimSpace(bundle.ModelRaw)
			} else {
				bundle.ModelName = getModelName(bundle.Index)
			}
			bundle.PromptIndex = resolvePromptIndex(bundle.Index, promptIndices)
			rollouts = append(rollouts, *bundle)
		}
	}

	report.RolloutCount = len(rollouts)
	return prompts, rollouts, report
}

func getModelName(index int) string {
	if index <= len(modelOrder) {
		return modelOrder[index-1]
	}
	if (index-len(modelOrder))%2 == 1 {
		return "GLM-5.1"
	}
	return "Qwen3.6-Plus"
}

func getFieldByID(fields []TableField, fieldID string) *TableField {
	for i := range fields {
		if fields[i].ID == fieldID {
			return &fields[i]
		}
	}
	return nil
}

func getFieldByName(fields []TableField, candidates ...string) *TableField {
	for i := range fields {
		fieldName := normalizeOptionText(fields[i].Name)
		for _, candidate := range candidates {
			cand := normalizeOptionText(candidate)
			if cand == "" {
				continue
			}
			if fieldName == cand || strings.Contains(fieldName, cand) {
				return &fields[i]
			}
		}
	}
	return nil
}

func normalizeOptionText(s string) string {
	replacer := strings.NewReplacer(" ", "", "-", "", "_", "", ".", "", "/", "", "\\", "", "（", "", "）", "", "(", "", ")", "", ":", "", "：", "", ",", "", "，", "")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(s)))
}

func matchSelectOptionByCandidates(field *TableField, candidates ...string) string {
	if field == nil || len(field.Options) == 0 {
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate) != "" {
				return strings.TrimSpace(candidate)
			}
		}
		return ""
	}
	for _, candidate := range candidates {
		candNorm := normalizeOptionText(candidate)
		if candNorm == "" {
			continue
		}
		for _, option := range field.Options {
			optNorm := normalizeOptionText(option.Name)
			if optNorm == candNorm || strings.HasPrefix(optNorm, candNorm) || strings.HasPrefix(candNorm, optNorm) || strings.Contains(optNorm, candNorm) || strings.Contains(candNorm, optNorm) {
				return option.Name
			}
		}
	}
	return ""
}

func taskTypeCandidates(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	normalized := normalizeOptionText(trimmed)
	switch {
	case strings.Contains(normalized, "bug") || strings.Contains(normalized, "修复") || strings.Contains(normalized, "调试"):
		return []string{trimmed, "Bug 修复 / 调试", "BUG修复", "bug"}
	case strings.Contains(normalized, "功能") || strings.Contains(normalized, "实现") || strings.Contains(normalized, "迭代"):
		return []string{trimmed, "功能迭代", "功能"}
	case strings.Contains(normalized, "重构"):
		return []string{trimmed, "代码重构", "重构"}
	case strings.Contains(normalized, "测试"):
		return []string{trimmed, "测试"}
	case strings.Contains(normalized, "理解") || strings.Contains(normalized, "分析"):
		return []string{trimmed, "代码理解与分析", "分析"}
	default:
		return []string{trimmed}
	}
}

func resolvedModelName(raw string, fields []TableField, fallback string) string {
	field := getFieldByID(fields, fieldModelName)
	value := matchSelectOptionByCandidates(field, raw, fallback)
	if strings.TrimSpace(value) != "" {
		return value
	}
	if strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	return strings.TrimSpace(fallback)
}

func parseBaseURL(baseURL string) (baseToken, tableID string, err error) {
	baseURL = strings.TrimSpace(baseURL)
	if strings.Contains(baseURL, "/base/") {
		parts := strings.Split(baseURL, "/base/")
		if len(parts) > 1 {
			remain := parts[1]
			idx := strings.IndexAny(remain, "?/")
			if idx > 0 {
				baseToken = remain[:idx]
			} else {
				baseToken = remain
			}
		}
	}
	if strings.Contains(baseURL, "table=") {
		parts := strings.Split(baseURL, "table=")
		if len(parts) > 1 {
			remain := parts[1]
			idx := strings.IndexAny(remain, "&?/")
			if idx > 0 {
				tableID = remain[:idx]
			} else {
				tableID = remain
			}
		}
	}
	if baseToken == "" {
		err = fmt.Errorf("无法从 URL 提取 base_token")
	}
	if tableID == "" {
		err = fmt.Errorf("无法从 URL 提取 table_id")
	}
	return
}

func upsertRecord(baseToken, tableID, recordID string, fields map[string]interface{}) (string, error) {
	tmpPath, tmpDir, tmpRef, err := writeTempJSON("lark-upsert", fields)
	if err != nil {
		return "", fmt.Errorf("写入临时文件失败: %v", err)
	}
	defer os.Remove(tmpPath)

	args := []string{"base", "+record-upsert", "--base-token", baseToken, "--table-id", tableID, "--json", "@" + tmpRef, "--as", "user"}
	if strings.TrimSpace(recordID) != "" {
		args = append(args, "--record-id", recordID)
	}

	out, err := runCmdInDir(tmpDir, "lark-cli", args...)
	if err != nil {
		return "", fmt.Errorf("%v, %s", err, string(out))
	}

	payload, err := jsonEnvelope(out)
	if err != nil {
		return "", fmt.Errorf("解析结果失败: %v, raw: %s", err, string(out))
	}

	var result struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			RecordID string `json:"record_id"`
			Record   struct {
				RecordIDList []string `json:"record_id_list"`
			} `json:"record"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return "", fmt.Errorf("解析结果失败: %v, raw: %s", err, string(out))
	}
	if !result.Ok {
		return "", fmt.Errorf(result.Error.Message)
	}
	if strings.TrimSpace(result.Data.RecordID) != "" {
		return strings.TrimSpace(result.Data.RecordID), nil
	}
	if len(result.Data.Record.RecordIDList) > 0 && strings.TrimSpace(result.Data.Record.RecordIDList[0]) != "" {
		return strings.TrimSpace(result.Data.Record.RecordIDList[0]), nil
	}
	return strings.TrimSpace(recordID), nil
}

func uploadPatchAttachment(baseToken, tableID, recordID, patchPath string) error {
	stagedPath, patchDir, patchFile, err := stageFileForCLI(patchPath)
	if err != nil {
		return fmt.Errorf("准备附件文件失败: %v", err)
	}
	if strings.TrimSpace(patchDir) != "" && strings.HasPrefix(filepath.Clean(patchDir), filepath.Clean(os.TempDir())) {
		defer os.RemoveAll(patchDir)
	} else if strings.TrimSpace(stagedPath) != "" {
		defer os.Remove(stagedPath)
	}
	if patchFile == "" {
		return nil
	}
	args := []string{"base", "+record-upload-attachment", "--base-token", baseToken, "--table-id", tableID, "--record-id", recordID, "--field-id", fieldGitDiff, "--file", patchFile, "--as", "user"}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		out, runErr := runCmdInDir(patchDir, "lark-cli", args...)
		if runErr != nil {
			lastErr = fmt.Errorf("%v, %s", runErr, string(out))
			if strings.Contains(strings.ToLower(lastErr.Error()), "timeout") && attempt < 2 {
				time.Sleep(time.Second * time.Duration(attempt+1))
				continue
			}
			return lastErr
		}

		payload, parseEnvelopeErr := jsonEnvelope(out)
		if parseEnvelopeErr != nil {
			lastErr = fmt.Errorf("解析附件上传结果失败: %v, raw: %s", parseEnvelopeErr, string(out))
			return lastErr
		}

		var result struct {
			Ok    bool `json:"ok"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			lastErr = fmt.Errorf("解析附件上传结果失败: %v, raw: %s", err, string(out))
			return lastErr
		}
		if result.Ok {
			return nil
		}
		lastErr = fmt.Errorf(result.Error.Message)
		if strings.Contains(strings.ToLower(lastErr.Error()), "timeout") && attempt < 2 {
			time.Sleep(time.Second * time.Duration(attempt+1))
			continue
		}
		return lastErr
	}
	return lastErr
}

func writeTempJSON(prefix string, value interface{}) (string, string, string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", "", "", err
	}
	tempDir := os.TempDir()
	name := fmt.Sprintf("%s-%d.json", prefix, os.Getpid())
	path := filepath.Join(tempDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", "", "", err
	}
	return path, tempDir, ".\\" + name, nil
}

func relativeFileRef(path string) (string, string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", ""
	}
	return filepath.Dir(trimmed), ".\\" + filepath.Base(trimmed)
}

func stageFileForCLI(path string) (string, string, string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", "", "", nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return "", "", "", err
	}
	base := filepath.Base(trimmed)
	stageDir, err := os.MkdirTemp(os.TempDir(), "stage-*")
	if err != nil {
		return "", "", "", err
	}
	tempPath := filepath.Join(stageDir, base)
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		os.RemoveAll(stageDir)
		return "", "", "", err
	}
	_, ref := relativeFileRef(tempPath)
	return tempPath, stageDir, ref, nil
}

func cellToStrings(value interface{}) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}
	case int:
		return []string{strconv.Itoa(typed)}
	case []interface{}:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			switch v := item.(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					items = append(items, strings.TrimSpace(v))
				}
			case map[string]interface{}:
				for _, key := range []string{"text", "name", "id", "file_token"} {
					if raw, ok := v[key]; ok {
						if s := strings.TrimSpace(fmt.Sprintf("%v", raw)); s != "" {
							items = append(items, s)
							break
						}
					}
				}
			default:
				s := strings.TrimSpace(fmt.Sprintf("%v", v))
				if s != "" && s != "<nil>" {
					items = append(items, s)
				}
			}
		}
		return items
	case map[string]interface{}:
		for _, key := range []string{"text", "name", "id", "file_token"} {
			if raw, ok := typed[key]; ok {
				if s := strings.TrimSpace(fmt.Sprintf("%v", raw)); s != "" {
					return []string{s}
				}
			}
		}
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" || text == "<nil>" {
		return nil
	}
	return []string{text}
}

func cellToString(value interface{}) string {
	items := cellToStrings(value)
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

func listTableFields(baseToken, tableID string) ([]TableField, error) {
	args := []string{
		"base", "+field-list",
		"--base-token", baseToken,
		"--table-id", tableID,
		"--format", "json",
		"--as", "user",
	}
	out, err := runCmd("lark-cli", args...)
	if err != nil {
		return nil, fmt.Errorf("查询字段失败: %v, %s", err, string(out))
	}
	payload, err := jsonEnvelope(out)
	if err != nil {
		return nil, fmt.Errorf("解析字段结果失败: %v, raw: %s", err, string(out))
	}
	var result struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			Items  []TableField `json:"items"`
			Fields []TableField `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("解析字段结果失败: %v, raw: %s", err, string(out))
	}
	if !result.Ok {
		return nil, fmt.Errorf(result.Error.Message)
	}
	if len(result.Data.Fields) > 0 {
		return result.Data.Fields, nil
	}
	return result.Data.Items, nil
}

func resolveParentRecordID(baseToken, tableID, input string) (string, string, error) {
	resolved := normalizeRecordID(input)
	if strings.HasPrefix(strings.ToLower(resolved), "rec") {
		return resolved, "record_id", nil
	}
	fields, err := listTableFields(baseToken, tableID)
	if err != nil {
		return "", "", err
	}
	candidateFieldIDs := []string{}
	for _, field := range fields {
		name := strings.ToLower(strings.TrimSpace(field.Name))
		if field.Type == "auto_number" || strings.Contains(name, "id") || strings.Contains(name, "编号") || strings.Contains(name, "序号") {
			candidateFieldIDs = append(candidateFieldIDs, field.ID)
		}
	}
	if len(candidateFieldIDs) == 0 {
		return "", "", fmt.Errorf("父级记录输入 %q 不是 record_id，且表中未找到可用于解析的 ID 字段", input)
	}
	const pageLimit = 200
	for offset := 0; ; offset += pageLimit {
		args := []string{
			"base", "+record-list",
			"--base-token", baseToken,
			"--table-id", tableID,
			"--field-id", fieldParentLink,
			"--limit", strconv.Itoa(pageLimit),
			"--offset", strconv.Itoa(offset),
			"--format", "json",
			"--as", "user",
		}
		for _, fieldID := range candidateFieldIDs {
			args = append(args, "--field-id", fieldID)
		}
		out, err := runCmd("lark-cli", args...)
		if err != nil {
			return "", "", fmt.Errorf("查询父级记录候选失败: %v, %s", err, string(out))
		}
		payload, err := jsonEnvelope(out)
		if err != nil {
			return "", "", fmt.Errorf("解析父级记录候选失败: %v, raw: %s", err, string(out))
		}
		var result struct {
			Ok    bool `json:"ok"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Data struct {
				FieldIDList  []string        `json:"field_id_list"`
				Records      [][]interface{} `json:"records"`
				Rows         [][]interface{} `json:"data"`
				RecordIDList []string        `json:"record_id_list"`
				HasMore      bool            `json:"has_more"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			return "", "", fmt.Errorf("解析父级记录候选失败: %v, raw: %s", err, string(out))
		}
		if !result.Ok {
			return "", "", fmt.Errorf(result.Error.Message)
		}
		rows := result.Data.Records
		if len(rows) == 0 {
			rows = result.Data.Rows
		}
		for i, row := range rows {
			if i >= len(result.Data.RecordIDList) {
				continue
			}
			isTopLevel := true
			for j, fieldID := range result.Data.FieldIDList {
				if j >= len(row) {
					continue
				}
				if fieldID == fieldParentLink && len(cellToStrings(row[j])) > 0 {
					isTopLevel = false
					break
				}
			}
			if !isTopLevel {
				continue
			}
			for j, fieldID := range result.Data.FieldIDList {
				if j >= len(row) || fieldID == fieldParentLink {
					continue
				}
				for _, candidate := range cellToStrings(row[j]) {
					if identifiersMatch(resolved, candidate) {
						return result.Data.RecordIDList[i], fieldID, nil
					}
				}
			}
		}
		if !result.Data.HasMore {
			break
		}
	}
	return "", "", fmt.Errorf("未找到与父级输入 %q 对应的顶级记录，请输入 rec 开头的 Record ID 或表中的题目 id", input)
}

func listChildRecords(baseToken, tableID, parentRecordID string) ([]ExistingRecord, error) {
	filterPath, filterDir, filterRef, err := writeTempJSON("lark-filter", map[string]interface{}{
		"logic":      "and",
		"conditions": [][]interface{}{{fieldParentLink, "intersects", []map[string]string{{"id": parentRecordID}}}},
	})
	if err != nil {
		return nil, fmt.Errorf("创建过滤文件失败: %v", err)
	}
	defer os.Remove(filterPath)

	args := []string{
		"base", "+record-list",
		"--base-token", baseToken,
		"--table-id", tableID,
		"--field-id", fieldPromptIndex,
		"--field-id", fieldPrompt,
		"--field-id", fieldRolloutID,
		"--field-id", fieldSessionID,
		"--filter-json", "@" + filterRef,
		"--limit", "200",
		"--format", "json",
		"--as", "user",
	}
	out, err := runCmdInDir(filterDir, "lark-cli", args...)
	if err != nil {
		return nil, fmt.Errorf("查询记录失败: %v, %s", err, string(out))
	}

	payload, err := jsonEnvelope(out)
	if err != nil {
		return nil, fmt.Errorf("解析查询结果失败: %v, raw: %s", err, string(out))
	}

	var result struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			FieldIDList  []string        `json:"field_id_list"`
			Records      [][]interface{} `json:"records"`
			Rows         [][]interface{} `json:"data"`
			RecordIDList []string        `json:"record_id_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("解析查询结果失败: %v, raw: %s", err, string(out))
	}
	if !result.Ok {
		return nil, fmt.Errorf(result.Error.Message)
	}

	rows := result.Data.Records
	if len(rows) == 0 {
		rows = result.Data.Rows
	}
	records := make([]ExistingRecord, 0, len(rows))
	for i, row := range rows {
		record := ExistingRecord{}
		if i < len(result.Data.RecordIDList) {
			record.RecordID = result.Data.RecordIDList[i]
		}
		for j, fieldID := range result.Data.FieldIDList {
			if j >= len(row) {
				continue
			}
			switch fieldID {
			case fieldPromptIndex:
				value := cellToString(row[j])
				record.PromptIndex, _ = strconv.Atoi(value)
			case fieldPrompt:
				record.PromptText = normalizeText(cellToString(row[j]))
			case fieldRolloutID:
				record.RolloutID = cellToString(row[j])
			case fieldSessionID:
				record.SessionID = cellToString(row[j])
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func buildExistingPromptMap(records []ExistingRecord) map[string]ExistingRecord {
	mapped := make(map[string]ExistingRecord, len(records))
	for _, record := range records {
		if record.PromptIndex == 0 || record.PromptText == "" {
			continue
		}
		for _, key := range promptIdentityCandidates(record.PromptIndex, record.PromptText) {
			mapped[key] = record
		}
	}
	return mapped
}

func buildExistingRolloutMap(records []ExistingRecord) map[string]ExistingRecord {
	mapped := make(map[string]ExistingRecord, len(records))
	for _, record := range records {
		key := rolloutIdentityKey(record.RolloutID, record.SessionID)
		if key == "|" {
			continue
		}
		mapped[key] = record
	}
	return mapped
}

func listSnapshotsByParent(baseToken, tableID, parentRecordID string, fieldIDs []string) ([]RecordSnapshot, error) {
	filterPath, filterDir, filterRef, err := writeTempJSON("lark-filter", map[string]interface{}{
		"logic":      "and",
		"conditions": [][]interface{}{{fieldParentLink, "intersects", []map[string]string{{"id": parentRecordID}}}},
	})
	if err != nil {
		return nil, fmt.Errorf("创建过滤文件失败: %v", err)
	}
	defer os.Remove(filterPath)

	args := []string{
		"base", "+record-list",
		"--base-token", baseToken,
		"--table-id", tableID,
		"--filter-json", "@" + filterRef,
		"--limit", "200",
		"--format", "json",
		"--as", "user",
	}
	for _, fieldID := range fieldIDs {
		if strings.TrimSpace(fieldID) == "" {
			continue
		}
		args = append(args, "--field-id", fieldID)
	}
	out, err := runCmdInDir(filterDir, "lark-cli", args...)
	if err != nil {
		return nil, fmt.Errorf("查询记录失败: %v, %s", err, string(out))
	}

	payload, err := jsonEnvelope(out)
	if err != nil {
		return nil, fmt.Errorf("解析查询结果失败: %v, raw: %s", err, string(out))
	}

	var result struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Data struct {
			FieldIDList  []string        `json:"field_id_list"`
			Records      [][]interface{} `json:"records"`
			Rows         [][]interface{} `json:"data"`
			RecordIDList []string        `json:"record_id_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("解析查询结果失败: %v, raw: %s", err, string(out))
	}
	if !result.Ok {
		return nil, fmt.Errorf(result.Error.Message)
	}

	rows := result.Data.Records
	if len(rows) == 0 {
		rows = result.Data.Rows
	}
	items := make([]RecordSnapshot, 0, len(rows))
	for i, row := range rows {
		item := RecordSnapshot{Values: map[string]string{}}
		if i < len(result.Data.RecordIDList) {
			item.RecordID = result.Data.RecordIDList[i]
		}
		for j, fieldID := range result.Data.FieldIDList {
			if j >= len(row) {
				continue
			}
			item.Values[fieldID] = normalizeText(cellToString(row[j]))
		}
		items = append(items, item)
	}
	return items, nil
}

func containsQualifiedFalse(text string) bool {
	lower := strings.ToLower(normalizeText(text))
	if lower == "" {
		return false
	}
	patterns := []string{"\"qualified\":false", "\"qualified\": false", "qualified:false", "qualified: false", "qualified=false", "qualified = false"}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func containsScoreIssue(text string) bool {
	lower := strings.ToLower(normalizeText(text))
	if lower == "" {
		return false
	}
	patterns := []string{"不合理", "unreasonable", "不正确", "有误", "错误"}
	for _, pattern := range patterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func collectImportWarnings(tableFields []TableField, promptSnapshots []RecordSnapshot, rolloutSnapshotsByPrompt map[string][]RecordSnapshot) []ImportWarning {
	promptIndexByRecordID := map[string]int{}
	for _, snapshot := range promptSnapshots {
		if value := strings.TrimSpace(snapshot.Values[fieldPromptIndex]); value != "" {
			if index, err := strconv.Atoi(value); err == nil {
				promptIndexByRecordID[snapshot.RecordID] = index
			}
		}
	}

	warnings := make([]ImportWarning, 0)
	seen := map[string]struct{}{}
	addWarning := func(scope, message string) {
		key := scope + "|" + message
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		warnings = append(warnings, ImportWarning{Scope: scope, Message: message})
	}

	promptQAField := getFieldByName(tableFields, "prompt质检分数及意见")
	scoreQAField := getFieldByName(tableFields, "score质检及意见")
	modelField := getFieldByID(tableFields, fieldModelName)

	if promptQAField != nil {
		for _, snapshot := range promptSnapshots {
			if containsQualifiedFalse(snapshot.Values[promptQAField.ID]) {
				index := promptIndexByRecordID[snapshot.RecordID]
				addWarning("二级", fmt.Sprintf("Prompt %d 的 prompt质检分数及意见中 qualified=false", index))
			}
		}
		for promptRecordID, snapshots := range rolloutSnapshotsByPrompt {
			for _, snapshot := range snapshots {
				if containsQualifiedFalse(snapshot.Values[promptQAField.ID]) {
					addWarning("三级", fmt.Sprintf("Prompt %d / Rollout %s 的 prompt质检分数及意见中 qualified=false", promptIndexByRecordID[promptRecordID], snapshot.Values[fieldRolloutID]))
				}
			}
		}
	}

	if scoreQAField != nil {
		for promptRecordID, snapshots := range rolloutSnapshotsByPrompt {
			for _, snapshot := range snapshots {
				if containsScoreIssue(snapshot.Values[scoreQAField.ID]) {
					addWarning("三级", fmt.Sprintf("Prompt %d / Rollout %s 的 score质检及意见出现不合理", promptIndexByRecordID[promptRecordID], snapshot.Values[fieldRolloutID]))
				}
			}
		}
	}

	_ = modelField
	for promptRecordID, snapshots := range rolloutSnapshotsByPrompt {
		counts := map[string]int{}
		labels := map[string]string{}
		for _, snapshot := range snapshots {
			modelName := normalizeOptionText(snapshot.Values[fieldModelName])
			if modelName == "" {
				continue
			}
			counts[modelName]++
			if labels[modelName] == "" {
				labels[modelName] = snapshot.Values[fieldModelName]
			}
		}
		for key, count := range counts {
			if count > 1 {
				addWarning("重复模型", fmt.Sprintf("Prompt %d 下存在重复 model_name：%s（%d 次）", promptIndexByRecordID[promptRecordID], labels[key], count))
			}
		}
	}

	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Scope == warnings[j].Scope {
			return warnings[i].Message < warnings[j].Message
		}
		return warnings[i].Scope < warnings[j].Scope
	})
	return warnings
}

func parseAuthStatus(out []byte) (bool, error) {
	var status struct {
		Identity   string `json:"identity"`
		Identities struct {
			User struct {
				Available   bool   `json:"available"`
				Status      string `json:"status"`
				TokenStatus string `json:"tokenStatus"`
			} `json:"user"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return false, err
	}
	user := status.Identities.User
	if !user.Available {
		return false, nil
	}
	if strings.EqualFold(user.Status, "ready") {
		return true, nil
	}
	if strings.EqualFold(user.TokenStatus, "valid") {
		return true, nil
	}
	return false, nil
}

func getConfigStatus() (bool, map[string]interface{}, error) {
	out, err := runCmd("lark-cli", "config", "show")
	if err != nil {
		message := strings.TrimSpace(string(out))
		messageLower := strings.ToLower(message)
		if strings.Contains(messageLower, "not configured") || strings.Contains(messageLower, "not_configured") || strings.Contains(messageLower, "invalid config format: no apps") {
			return false, nil, nil
		}
		return false, nil, err
	}

	payload, envErr := jsonObjectEnvelope(out)
	if envErr != nil {
		return false, nil, envErr
	}

	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return false, nil, err
	}
	return true, data, nil
}

func handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	configured, configData, err := getConfigStatus()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "configured": configured, "config": configData})
}

func handleConfigInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID     string `json:"appId"`
		AppSecret string `json:"appSecret"`
		Brand     string `json:"brand"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "请求解析失败"})
		return
	}

	appID := strings.TrimSpace(req.AppID)
	appSecret := strings.TrimSpace(req.AppSecret)
	brand := strings.TrimSpace(req.Brand)
	if brand == "" {
		brand = "feishu"
	}
	if appID == "" || appSecret == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "请填写 App ID 和 App Secret"})
		return
	}

	cmd := newCommand("lark-cli", "config", "init", "--app-id", appID, "--app-secret-stdin", "--brand", brand)
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	cmd.Stdin = strings.NewReader(appSecret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = fmt.Sprintf("保存配置失败: %v", err)
		}
		writeJSON(w, map[string]interface{}{"ok": false, "error": message})
		return
	}

	configured, configData, statusErr := getConfigStatus()
	if statusErr != nil {
		writeJSON(w, map[string]interface{}{"ok": true, "configured": true})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "configured": configured, "config": configData})
}

func handleConfigStart(w http.ResponseWriter, r *http.Request) {
	configured, configData, err := getConfigStatus()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": err.Error()})
		return
	}
	if configured {
		writeJSON(w, map[string]interface{}{"ok": true, "configured": true, "config": configData})
		return
	}

	state, startErr := startConfigInitFlow()
	if startErr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": startErr.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":               true,
		"configured":       false,
		"verification_url": state.VerificationURL,
		"qr_available":     strings.TrimSpace(state.QRASCII) != "" || strings.TrimSpace(state.QRImageURL) != "",
		"qr_ascii":         state.QRASCII,
		"qr_image_url":     state.QRImageURL,
	})
}

func handleConfigComplete(w http.ResponseWriter, r *http.Request) {
	configured, configData, err := getConfigStatus()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": err.Error()})
		return
	}
	if configured {
		configInitStateMu.Lock()
		configInitStateCurrent.Configured = true
		configInitStateCurrent.Running = false
		configInitStateCurrent.Done = true
		configInitStateCurrent.Cmd = nil
		configInitStateCurrent.Error = ""
		configInitStateMu.Unlock()
		writeJSON(w, map[string]interface{}{"ok": true, "configured": true, "config": configData})
		return
	}

	configInitStateMu.Lock()
	state := configInitStateCurrent
	configInitStateMu.Unlock()
	if state.Error != "" && state.Done {
		writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": state.Error})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": false, "configured": false, "error": "请先在飞书页面完成应用创建或绑定，然后再点击“我已完成配置”"})
}

func handleConfigRemove(w http.ResponseWriter, r *http.Request) {
	stopConfigInitFlow()
	out, err := runCmd("lark-cli", "config", "remove")
	if err != nil {
		message := strings.TrimSpace(string(out))
		messageLower := strings.ToLower(message)
		if strings.Contains(messageLower, "not configured") || strings.Contains(messageLower, "not_configured") || strings.Contains(messageLower, "invalid config format: no apps") {
			writeJSON(w, map[string]interface{}{"ok": true, "configured": false, "authed": false})
			return
		}
		if message == "" {
			message = fmt.Sprintf("清除配置失败: %v", err)
		}
		writeJSON(w, map[string]interface{}{"ok": false, "error": message})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "configured": false, "authed": false})
}

func handleCheckAuth(w http.ResponseWriter, r *http.Request) {
	configured, _, cfgErr := getConfigStatus()
	if cfgErr != nil {
		writeJSON(w, map[string]interface{}{"authed": false, "configured": false, "error": cfgErr.Error()})
		return
	}
	if !configured {
		writeJSON(w, map[string]interface{}{"authed": false, "configured": false, "error": "请先完成飞书应用配置"})
		return
	}

	authed, err := getAuthStatus()
	if err != nil {
		writeJSON(w, map[string]interface{}{"authed": false, "configured": true})
		return
	}
	writeJSON(w, map[string]interface{}{"authed": authed, "configured": true})
}

func parseAuthLoginStart(out []byte) (string, string, error) {
	var result struct {
		Ok              *bool  `json:"ok"`
		DeviceCode      string `json:"device_code"`
		VerificationURL string `json:"verification_url"`
		Error           struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", "", fmt.Errorf("解析登录响应失败: %v", err)
	}
	if result.Ok != nil && !*result.Ok {
		if strings.TrimSpace(result.Error.Message) != "" {
			return "", "", fmt.Errorf(result.Error.Message)
		}
		return "", "", fmt.Errorf("登录请求失败")
	}
	if strings.TrimSpace(result.DeviceCode) == "" || strings.TrimSpace(result.VerificationURL) == "" {
		if strings.TrimSpace(result.Error.Message) != "" {
			return "", "", fmt.Errorf(result.Error.Message)
		}
		return "", "", fmt.Errorf("登录响应缺少 device_code 或 verification_url")
	}
	return result.DeviceCode, result.VerificationURL, nil
}

func handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	configured, _, cfgErr := getConfigStatus()
	if cfgErr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": cfgErr.Error()})
		return
	}
	if !configured {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "未完成飞书应用配置，请先点击“一键打开飞书配置”"})
		return
	}

	cmd := newCommand("lark-cli", "auth", "login", "--no-wait", "--json", "--domain", "base")
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	out, err := cmd.CombinedOutput()
	deviceCode, verificationURL, parseErr := parseAuthLoginStart(out)
	if parseErr != nil {
		message := parseErr.Error()
		if err != nil && strings.TrimSpace(message) == "" {
			message = fmt.Sprintf("登录失败: %v", err)
		}
		writeJSON(w, map[string]interface{}{"ok": false, "error": message})
		return
	}

	qrASCII, qrImageURL := generateQRCodePayload(verificationURL)

	writeJSON(w, map[string]interface{}{
		"ok":               true,
		"device_code":      deviceCode,
		"verification_url": verificationURL,
		"qr_available":     strings.TrimSpace(qrASCII) != "" || strings.TrimSpace(qrImageURL) != "",
		"qr_ascii":         qrASCII,
		"qr_image_url":     qrImageURL,
	})
}

func parseAuthLoginComplete(out []byte) (bool, string, error) {
	jsonStart := bytes.Index(out, []byte("{"))
	if jsonStart == -1 {
		return false, "", fmt.Errorf("未获取到授权响应")
	}

	var result struct {
		Ok    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out[jsonStart:], &result); err != nil {
		return false, "", fmt.Errorf("解析授权结果失败: %v", err)
	}
	if result.Ok {
		return true, "", nil
	}
	message := strings.TrimSpace(result.Error.Message)
	if message == "" {
		message = "授权尚未完成，请确认已在飞书中完成扫码并同意授权后重试"
	}
	return false, message, nil
}

func getAuthStatus() (bool, error) {
	cmd := newCommand("lark-cli", "auth", "status")
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return parseAuthStatus(out)
}

func handleAuthComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"deviceCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "请求解析失败"})
		return
	}
	if strings.TrimSpace(req.DeviceCode) == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "authed": false, "error": "缺少 deviceCode"})
		return
	}

	cmd := newCommand("lark-cli", "auth", "login", "--device-code", req.DeviceCode)
	cmd.Env = append(os.Environ(),
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr == nil {
		for range 5 {
			authed, statusErr := getAuthStatus()
			if statusErr == nil && authed {
				writeJSON(w, map[string]interface{}{"ok": true, "authed": true})
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	authed, message, parseErr := parseAuthLoginComplete(out)
	if parseErr != nil {
		for range 5 {
			statusAuthed, statusErr := getAuthStatus()
			if statusErr == nil && statusAuthed {
				writeJSON(w, map[string]interface{}{"ok": true, "authed": true})
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		if cmdErr != nil {
			message := strings.TrimSpace(string(out))
			if message == "" {
				message = cmdErr.Error()
			}
			writeJSON(w, map[string]interface{}{"ok": false, "authed": false, "error": message})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": false, "authed": false, "error": parseErr.Error()})
		return
	}
	if authed {
		writeJSON(w, map[string]interface{}{"ok": true, "authed": true})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": false, "authed": false, "error": message})
}

func scanDirEntry(path string) (int, int) {
	promptCount := 0
	rolloutIndexes := map[int]struct{}{}
	_ = filepath.WalkDir(path, func(current string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if promptFilePattern.MatchString(name) {
			promptCount++
			return nil
		}
		if match := rolloutFilePattern.FindStringSubmatch(name); match != nil {
			index, _ := strconv.Atoi(match[1])
			rolloutIndexes[index] = struct{}{}
		}
		return nil
	})
	return promptCount, len(rolloutIndexes)
}

func handleListDir(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "D:\\"
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	result := map[string]interface{}{"ok": true, "entries": []interface{}{}}
	for _, entry := range entries {
		item := map[string]interface{}{
			"name":  entry.Name(),
			"isDir": entry.IsDir(),
			"path":  filepath.Join(path, entry.Name()),
		}
		if entry.IsDir() {
			subPath := filepath.Join(path, entry.Name())
			promptCount, rolloutCount := scanDirEntry(subPath)
			if promptCount > 0 || rolloutCount > 0 {
				item["hasImportable"] = true
				item["promptFileCount"] = promptCount
				item["rolloutGroupCount"] = rolloutCount
			}
		}
		result["entries"] = append(result["entries"].([]interface{}), item)
	}

	writeJSON(w, result)
}

func handlePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FolderPath     string `json:"folderPath"`
		BaseURL        string `json:"baseURL"`
		ParentRecordID string `json:"parentRecordId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": fmt.Sprintf("请求解析失败: %v", err)})
		return
	}

	req.ParentRecordID = normalizeRecordID(req.ParentRecordID)

	prompts, rollouts, report := parseFolder(req.FolderPath)
	baseToken, tableID, err := parseBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error(), "scan": report})
		return
	}
	resolvedParentRecordID, resolvedBy, err := resolveParentRecordID(baseToken, tableID, req.ParentRecordID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error(), "scan": report})
		return
	}
	appendImportDebugLog(map[string]interface{}{"stage": "preview_resolve", "inputParentRecordId": req.ParentRecordID, "resolvedParentRecordId": resolvedParentRecordID, "resolvedBy": resolvedBy, "baseURL": req.BaseURL, "folderPath": req.FolderPath})
	req.ParentRecordID = resolvedParentRecordID

	existingPromptRecords, err := listChildRecords(baseToken, tableID, req.ParentRecordID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error(), "scan": report})
		return
	}
	existingPromptMap := buildExistingPromptMap(existingPromptRecords)

	promptRecordIDs := map[string]string{}
	promptRecordByID := map[string]PromptFile{}
	for _, prompt := range prompts {
		if existing, ok := findExistingPromptRecord(existingPromptMap, prompt); ok {
			promptRecordIDs[promptGroupKey(prompt.GroupPath, prompt.Index)] = existing.RecordID
			promptRecordByID[existing.RecordID] = prompt
		}
	}

	rolloutByPromptRecord := map[string]map[string]ExistingRecord{}
	for recordID := range promptRecordByID {
		children, childErr := listChildRecords(baseToken, tableID, recordID)
		if childErr != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": childErr.Error(), "scan": report})
			return
		}
		rolloutByPromptRecord[recordID] = buildExistingRolloutMap(children)
	}

	pendingRolloutCount := 0
	data := make([]RolloutMapping, 0, len(rollouts))
	for _, rollout := range rollouts {
		promptKey := promptGroupKey(rollout.GroupPath, rollout.PromptIndex)
		promptRecordID := promptRecordIDs[promptKey]
		recordID := ""
		if promptRecordID != "" {
			if existingMap, ok := rolloutByPromptRecord[promptRecordID]; ok {
				if existing, ok := existingMap[rolloutIdentityKey(rollout.RolloutID, rollout.SessionID)]; ok {
					recordID = existing.RecordID
				}
			}
		}
		if recordID == "" {
			pendingRolloutCount++
		}
		mapping := RolloutMapping{
			Index:          rollout.Index,
			RolloutID:      rollout.RolloutID,
			PromptIndex:    rollout.PromptIndex,
			ModelName:      rollout.ModelName,
			Score:          rollout.Score,
			Reason:         rollout.Reason,
			SessionID:      rollout.SessionID,
			PatchPath:      rollout.PatchPath,
			PromptRecordID: promptRecordID,
			RecordID:       recordID,
			SourceDir:      pathLabel(req.FolderPath, rollout.GroupPath),
		}
		if promptRecord, ok := promptRecordByID[promptRecordID]; ok {
			mapping.PromptPath = pathLabel(req.FolderPath, promptRecord.Path)
			mapping.PromptText = promptRecord.Content
		}
		data = append(data, mapping)
	}

	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"version": appVersion,
		"data":    data,
		"meta": map[string]interface{}{
			"datasetCount":        report.DatasetCount,
			"promptCount":         len(prompts),
			"rolloutCount":        len(rollouts),
			"pendingRolloutCount": pendingRolloutCount,
			"scan":                report,
		},
	})
}

func handleImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FolderPath     string           `json:"folderPath"`
		BaseURL        string           `json:"baseURL"`
		ParentRecordID string           `json:"parentRecordId"`
		Mappings       []RolloutMapping `json:"mappings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": fmt.Sprintf("请求解析失败: %v", err)})
		return
	}
	req.ParentRecordID = normalizeRecordID(req.ParentRecordID)
	appendImportDebugLog(map[string]interface{}{"stage": "request", "folderPath": req.FolderPath, "baseURL": req.BaseURL, "parentRecordId": req.ParentRecordID})
	if strings.TrimSpace(req.ParentRecordID) == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": "请填写父级 Record ID"})
		return
	}

	baseToken, tableID, err := parseBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error()})
		return
	}
	resolvedParentRecordID, resolvedBy, err := resolveParentRecordID(baseToken, tableID, req.ParentRecordID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error()})
		return
	}
	appendImportDebugLog(map[string]interface{}{"stage": "import_resolve", "inputParentRecordId": req.ParentRecordID, "resolvedParentRecordId": resolvedParentRecordID, "resolvedBy": resolvedBy, "baseURL": req.BaseURL, "folderPath": req.FolderPath})
	req.ParentRecordID = resolvedParentRecordID

	tableFields, err := listTableFields(baseToken, tableID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error()})
		return
	}
	if _, taskCountErr := upsertRecord(baseToken, tableID, req.ParentRecordID, map[string]interface{}{fieldTaskCount: "7"}); taskCountErr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": fmt.Sprintf("设置一级 task_count 失败: %v", taskCountErr)})
		return
	}

	prompts, rollouts, report := parseFolder(req.FolderPath)
	if len(prompts) == 0 && len(rollouts) == 0 {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": "目录下未发现可导入的数据文件", "scan": report})
		return
	}

	existingPromptRecords, err := listChildRecords(baseToken, tableID, req.ParentRecordID)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": err.Error(), "scan": report})
		return
	}
	existingPromptMap := buildExistingPromptMap(existingPromptRecords)

	results := make([]ImportResult, 0, len(prompts)+len(rollouts))
	promptRecordIDs := map[string]string{}
	promptByGroupKey := map[string]PromptFile{}
	rolloutByPromptRecord := map[string]map[string]ExistingRecord{}
	categoryField := getFieldByID(tableFields, fieldCategory)
	difficultyField := getFieldByID(tableFields, fieldDifficulty)

	for _, prompt := range prompts {
		groupKey := promptGroupKey(prompt.GroupPath, prompt.Index)
		promptByGroupKey[groupKey] = prompt
		label := pathLabel(req.FolderPath, prompt.GroupPath)
		existingRecordID := ""
		action := "创建"
		if existing, ok := findExistingPromptRecord(existingPromptMap, prompt); ok {
			existingRecordID = existing.RecordID
			action = "更新"
		}

		fields := map[string]interface{}{
			fieldPromptIndex: strconv.Itoa(prompt.Index),
			fieldPrompt:      promptFieldValue(prompt.Content),
			fieldParentLink:  []map[string]string{{"id": req.ParentRecordID}},
			fieldTechStack:   strings.TrimSpace(prompt.TechStack),
			fieldModuleTags:  strings.TrimSpace(prompt.ModuleTags),
		}
		categoryCandidates := []string{}
		if strings.TrimSpace(prompt.Category) != "" {
			categoryCandidates = append(categoryCandidates, prompt.Category)
		}
		categoryCandidates = append(categoryCandidates, taskTypeCandidates(prompt.TaskType)...)
		if value := matchSelectOptionByCandidates(categoryField, categoryCandidates...); strings.TrimSpace(value) != "" {
			fields[fieldCategory] = value
		}
		if value := matchSelectOptionByCandidates(difficultyField, prompt.Difficulty); strings.TrimSpace(value) != "" {
			fields[fieldDifficulty] = value
		}
		recordID, upsertErr := upsertRecord(baseToken, tableID, existingRecordID, fields)
		if upsertErr != nil {
			results = append(results, ImportResult{Index: prompt.Index, Success: false, Message: fmt.Sprintf("%s / Prompt %d %s失败: %v", label, prompt.Index, action, upsertErr)})
			continue
		}
		promptRecordIDs[groupKey] = recordID
		results = append(results, ImportResult{Index: prompt.Index, RecordID: recordID, Success: true, Message: fmt.Sprintf("%s / Prompt %d 已%s", label, prompt.Index, action)})
	}

	for _, recordID := range promptRecordIDs {
		children, childErr := listChildRecords(baseToken, tableID, recordID)
		if childErr != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": childErr.Error(), "scan": report})
			return
		}
		rolloutByPromptRecord[recordID] = buildExistingRolloutMap(children)
	}

	for _, rollout := range rollouts {
		groupKey := promptGroupKey(rollout.GroupPath, rollout.PromptIndex)
		promptRecordID := promptRecordIDs[groupKey]
		prompt := promptByGroupKey[groupKey]
		label := pathLabel(req.FolderPath, rollout.GroupPath)
		if promptRecordID == "" {
			results = append(results, ImportResult{Index: rollout.Index, Success: false, Message: fmt.Sprintf("%s / Rollout %s 失败: 对应 Prompt %d 未创建", label, rollout.RolloutID, rollout.PromptIndex)})
			continue
		}

		existingRecordID := ""
		if existingMap, ok := rolloutByPromptRecord[promptRecordID]; ok {
			if existing, ok := existingMap[rolloutIdentityKey(rollout.RolloutID, rollout.SessionID)]; ok {
				existingRecordID = existing.RecordID
			}
		}

		fields := map[string]interface{}{
			fieldRolloutID:   rollout.RolloutID,
			fieldSessionID:   rollout.SessionID,
			fieldScore:       rollout.Score,
			fieldScoreReason: rollout.Reason,
			fieldModelName:   resolvedModelName(rollout.ModelRaw, tableFields, rollout.ModelName),
			fieldPrompt:      promptFieldValue(prompt.Content),
			fieldParentLink:  []map[string]string{{"id": promptRecordID}},
		}
		recordID, upsertErr := upsertRecord(baseToken, tableID, existingRecordID, fields)
		if upsertErr != nil {
			results = append(results, ImportResult{Index: rollout.Index, Success: false, Message: fmt.Sprintf("%s / Rollout %s 写入失败: %v", label, rollout.RolloutID, upsertErr)})
			continue
		}

		action := "已创建"
		if existingRecordID != "" {
			action = "已更新"
		}
		message := fmt.Sprintf("%s / Rollout %s %s", label, rollout.RolloutID, action)
		if rollout.PatchPath != "" {
			if attachErr := uploadPatchAttachment(baseToken, tableID, recordID, rollout.PatchPath); attachErr != nil {
				results = append(results, ImportResult{Index: rollout.Index, RecordID: recordID, Success: false, Message: fmt.Sprintf("%s / Rollout %s %s，但附件上传失败: %v", label, rollout.RolloutID, action, attachErr)})
				continue
			}
			message += "，patch 已上传到 git_diff"
		}
		results = append(results, ImportResult{Index: rollout.Index, RecordID: recordID, Success: true, Message: message})
	}

	successCount := 0
	for _, result := range results {
		if result.Success {
			successCount++
		}
	}

	warningFieldIDs := []string{fieldPromptIndex}
	if field := getFieldByName(tableFields, "prompt质检分数及意见"); field != nil {
		warningFieldIDs = append(warningFieldIDs, field.ID)
	}
	promptSnapshots, warningErr := listSnapshotsByParent(baseToken, tableID, req.ParentRecordID, warningFieldIDs)
	if warningErr != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": warningErr.Error(), "scan": report})
		return
	}
	rolloutWarningFields := []string{fieldRolloutID, fieldModelName}
	if field := getFieldByName(tableFields, "prompt质检分数及意见"); field != nil {
		rolloutWarningFields = append(rolloutWarningFields, field.ID)
	}
	if field := getFieldByName(tableFields, "score质检及意见"); field != nil {
		rolloutWarningFields = append(rolloutWarningFields, field.ID)
	}
	rolloutSnapshotsByPrompt := map[string][]RecordSnapshot{}
	for _, promptSnapshot := range promptSnapshots {
		snapshots, childErr := listSnapshotsByParent(baseToken, tableID, promptSnapshot.RecordID, rolloutWarningFields)
		if childErr != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "version": appVersion, "error": childErr.Error(), "scan": report})
			return
		}
		rolloutSnapshotsByPrompt[promptSnapshot.RecordID] = snapshots
	}
	warnings := collectImportWarnings(tableFields, promptSnapshots, rolloutSnapshotsByPrompt)

	appendImportDebugLog(map[string]interface{}{"stage": "response", "folderPath": req.FolderPath, "parentRecordId": req.ParentRecordID, "successCount": successCount, "failCount": len(results) - successCount, "warningCount": len(warnings), "total": len(results)})
	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"version":      appVersion,
		"results":      results,
		"warnings":     warnings,
		"warningCount": len(warnings),
		"total":        len(results),
		"successCount": successCount,
		"failCount":    len(results) - successCount,
		"scan":         report,
	})
}

func main() {
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}

	http.Handle("/", withNoCache(http.FileServer(http.FS(staticRoot))))
	http.HandleFunc("/api/config/status", handleConfigStatus)
	http.HandleFunc("/api/config/init", handleConfigInit)
	http.HandleFunc("/api/config/start", handleConfigStart)
	http.HandleFunc("/api/config/complete", handleConfigComplete)
	http.HandleFunc("/api/config/remove", handleConfigRemove)
	http.HandleFunc("/api/check-auth", handleCheckAuth)
	http.HandleFunc("/api/auth/login", handleAuthLogin)
	http.HandleFunc("/api/auth/complete", handleAuthComplete)
	http.HandleFunc("/api/list-dir", handleListDir)
	http.HandleFunc("/api/preview", handlePreview)
	http.HandleFunc("/api/import", handleImport)

	fmt.Println("🚀 数据回填工具已启动: http://localhost:8081")
	if err := http.ListenAndServe(":8081", nil); err != nil {
		panic(err)
	}
}
