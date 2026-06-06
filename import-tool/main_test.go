package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseScoreFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "01.score")
	content := "Model: doubao\n分数: 8\n理由: 第一行理由\n第二行理由"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	score, reason, model, err := parseScoreDetails(path)
	if err != nil {
		t.Fatalf("parseScoreDetails returned error: %v", err)
	}
	if score != "8" {
		t.Fatalf("score = %q, want %q", score, "8")
	}
	if reason != "第一行理由\n第二行理由" {
		t.Fatalf("reason = %q, want %q", reason, "第一行理由\n第二行理由")
	}
	if model != "doubao" {
		t.Fatalf("model = %q, want %q", model, "doubao")
	}
}

func TestParsePromptMetadata(t *testing.T) {
	content := "Prompt 1\n\n任务类型: bug修复\n分类: 功能实现\n难度: 困难\n任务标签: 侧边栏导航修复\n涉及模块: src/components/Sidebar.vue"
	taskType, category, difficulty, moduleTags, techStack := parsePromptMetadata(content)
	if taskType != "bug修复" || category != "功能实现" || difficulty != "困难" || moduleTags != "侧边栏导航修复" || techStack != "src/components/Sidebar.vue" {
		t.Fatalf("unexpected parsed prompt metadata: %q %q %q %q %q", taskType, category, difficulty, moduleTags, techStack)
	}
}

func TestCleanPromptContent(t *testing.T) {
	content := "Prompt 1\n\n任务类型: bug修复\n分类: 功能实现\n难度: 困难\n任务标签: 侧边栏导航修复\n涉及模块: src/components/Sidebar.vue\n\n这里是正文第一段。\n\n这里是正文第二段。\n验证方式: 这一段不要导入"
	got := cleanPromptContent(content)
	want := "这里是正文第一段。\n\n这里是正文第二段。"
	if got != want {
		t.Fatalf("cleanPromptContent = %q, want %q", got, want)
	}
}

func TestParseAuthStatus(t *testing.T) {
	authed, err := parseAuthStatus([]byte(`{"identity":"user","identities":{"user":{"available":true,"status":"ready","tokenStatus":"valid"}}}`))
	if err != nil {
		t.Fatalf("parseAuthStatus returned error: %v", err)
	}
	if !authed {
		t.Fatal("authed = false, want true")
	}
}

func TestParseAuthStatusNotAuthed(t *testing.T) {
	authed, err := parseAuthStatus([]byte(`{"identity":"bot","identities":{"user":{"available":false,"status":"","tokenStatus":""}}}`))
	if err != nil {
		t.Fatalf("parseAuthStatus returned error: %v", err)
	}
	if authed {
		t.Fatal("authed = true, want false")
	}
}

func TestWriteTempJSONUsesTempDirectoryRelativeRef(t *testing.T) {
	path, dir, ref, err := writeTempJSON("case", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("writeTempJSON returned error: %v", err)
	}
	defer os.Remove(path)

	if filepath.Dir(path) != dir {
		t.Fatalf("path dir = %q, want %q", filepath.Dir(path), dir)
	}
	if dir != os.TempDir() {
		t.Fatalf("dir = %q, want %q", dir, os.TempDir())
	}
	wantRef := "." + string(filepath.Separator) + "case-" + strconv.Itoa(os.Getpid()) + ".json"
	if ref != wantRef {
		t.Fatalf("ref = %q, want %q", ref, wantRef)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file missing: %v", err)
	}
}

func TestRelativeFileRefReturnsDirectoryAndRelativeFile(t *testing.T) {
	path := filepath.Join(`C:\data`, `repo-1`, `01.patch`)
	dir, ref := relativeFileRef(path)
	if dir != filepath.Join(`C:\data`, `repo-1`) {
		t.Fatalf("dir = %q", dir)
	}
	wantRef := "." + string(filepath.Separator) + "01.patch"
	if ref != wantRef {
		t.Fatalf("ref = %q, want %q", ref, wantRef)
	}
}

func TestStageFileForCLIUsesTempDir(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "01.patch")
	content := "diff --git a b"
	if err := os.WriteFile(source, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	stagedPath, stagedDir, stagedRef, err := stageFileForCLI(source)
	if err != nil {
		t.Fatalf("stageFileForCLI returned error: %v", err)
	}
	defer os.RemoveAll(stagedDir)
	if filepath.Dir(stagedDir) != os.TempDir() {
		t.Fatalf("staged parent dir = %q, want %q", filepath.Dir(stagedDir), os.TempDir())
	}
	if filepath.Dir(stagedPath) != stagedDir {
		t.Fatalf("stagedPath dir = %q, want %q", filepath.Dir(stagedPath), stagedDir)
	}
	if filepath.Base(stagedPath) != "01.patch" {
		t.Fatalf("staged filename = %q", filepath.Base(stagedPath))
	}
	wantRef2 := "." + string(filepath.Separator) + "01.patch"
	if stagedRef != wantRef2 {
		t.Fatalf("stagedRef = %q, want %q", stagedRef, wantRef2)
	}
	data, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(data) != content {
		t.Fatalf("staged content = %q, want %q", string(data), content)
	}
}

func TestNormalizeRecordIDStripsInvisibleCharacters(t *testing.T) {
	input := " \u200brecvlhhjo5CI5O\ufeff\n"
	got := normalizeRecordID(input)
	if got != "recvlhhjo5CI5O" {
		t.Fatalf("normalizeRecordID = %q", got)
	}
}

func TestNormalizeRecordIDExtractsFromMixedText(t *testing.T) {
	input := "父记录: recvlhhjo5CI5O 已复制"
	got := normalizeRecordID(input)
	if got != "recvlhhjo5CI5O" {
		t.Fatalf("normalizeRecordID = %q", got)
	}
}

func TestIdentifiersMatchAllowsLeadingZeros(t *testing.T) {
	if !identifiersMatch("1", "001") {
		t.Fatalf("expected identifiers to match")
	}
	if identifiersMatch("1", "002") {
		t.Fatalf("expected identifiers not to match")
	}
}

func TestResolvedModelNameMatchesSelectPrefixCaseInsensitive(t *testing.T) {
	fields := []TableField{{ID: fieldModelName, Options: []FieldOption{{Name: "Doubao-Seed-2.0-Code"}, {Name: "GPT5.4"}}}}
	got := resolvedModelName("doubao", fields, "GPT5.4")
	if got != "Doubao-Seed-2.0-Code" {
		t.Fatalf("resolvedModelName = %q", got)
	}
}

func TestPromptFieldValueKeepsFullPrompt(t *testing.T) {
	input := strings.Repeat("测", legacyPromptFieldRunes+25)
	got := promptFieldValue(input)
	if got != input {
		t.Fatalf("promptFieldValue changed prompt length: got %d, want %d", len([]rune(got)), len([]rune(input)))
	}
}

func TestLegacyPromptFieldValueTruncatesForCompatibility(t *testing.T) {
	input := strings.Repeat("测", legacyPromptFieldRunes+25)
	got := legacyPromptFieldValue(input)
	if len([]rune(got)) != legacyPromptFieldRunes {
		t.Fatalf("len([]rune(got)) = %d, want %d", len([]rune(got)), legacyPromptFieldRunes)
	}
}

func TestPromptIdentityKeyUsesStoredPromptValue(t *testing.T) {
	input := strings.Repeat("a", legacyPromptFieldRunes+10)
	got := promptIdentityKey(3, input)
	want := strconv.Itoa(3) + "|" + promptFieldValue(input)
	if got != want {
		t.Fatalf("promptIdentityKey = %q, want %q", got, want)
	}
}

func TestFindExistingPromptRecordMatchesLegacyTruncatedPrompt(t *testing.T) {
	input := strings.Repeat("a", legacyPromptFieldRunes+10)
	existingPromptMap := buildExistingPromptMap([]ExistingRecord{{
		RecordID:    "rec_prompt_1",
		PromptIndex: 3,
		PromptText:  legacyPromptFieldValue(input),
	}})
	prompt := PromptFile{Index: 3, Content: input}
	got, ok := findExistingPromptRecord(existingPromptMap, prompt)
	if !ok {
		t.Fatal("expected legacy truncated prompt to match full prompt")
	}
	if got.RecordID != "rec_prompt_1" {
		t.Fatalf("recordID = %q, want %q", got.RecordID, "rec_prompt_1")
	}
}

func TestParseFolderRecursiveGroups(t *testing.T) {
	root := t.TempDir()
	repo1 := filepath.Join(root, "repo-1")
	repo2 := filepath.Join(root, "nested", "repo-2")
	for _, dir := range []string{repo1, repo2} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	files := map[string]string{
		filepath.Join(repo1, "prompt-1.txt"):     "repo1 prompt 1",
		filepath.Join(repo1, "prompt-2.txt"):     "repo1 prompt 2",
		filepath.Join(repo1, "01.id"):            "session-repo1-01",
		filepath.Join(repo1, "01.score"):         "Model: doubao\nScore: 7\nReason: repo1 reason 01",
		filepath.Join(repo1, "01.patch"):         "diff --git a b",
		filepath.Join(repo1, "01.log"):           "ignore me",
		filepath.Join(repo1, "06.id"):            "session-repo1-06",
		filepath.Join(repo1, "06.score"):         "分数: 9\n理由: repo1 reason 06",
		filepath.Join(repo1, "06.patch"):         "diff --git c d",
		filepath.Join(repo1, "slot-models.json"): "{}",
		filepath.Join(repo1, "repo.zip"):         "zip",
		filepath.Join(repo2, "prompt-1.txt"):     "repo2 prompt 1",
		filepath.Join(repo2, "01.id"):            "session-repo2-01",
		filepath.Join(repo2, "01.score"):         "分数: 5\n理由: repo2 reason 01",
		filepath.Join(repo2, "01.patch"):         "diff --git e f",
	}

	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	prompts, rollouts, report := parseFolder(root)

	if report.DatasetCount != 2 {
		t.Fatalf("datasetCount = %d, want 2", report.DatasetCount)
	}
	if report.PromptFileCount != 3 {
		t.Fatalf("promptFileCount = %d, want 3", report.PromptFileCount)
	}
	if report.RolloutCount != 3 {
		t.Fatalf("rolloutCount = %d, want 3", report.RolloutCount)
	}
	if report.IgnoredFileCount != 3 {
		t.Fatalf("ignoredFileCount = %d, want 3", report.IgnoredFileCount)
	}
	if len(prompts) != 3 {
		t.Fatalf("len(prompts) = %d, want 3", len(prompts))
	}
	if len(rollouts) != 3 {
		t.Fatalf("len(rollouts) = %d, want 3", len(rollouts))
	}

	foundRepo1First := false
	foundRepo1SecondPrompt := false
	foundRepo2 := false

	for _, rollout := range rollouts {
		switch rollout.SessionID {
		case "session-repo1-01":
			foundRepo1First = true
			if rollout.PromptIndex != 1 {
				t.Fatalf("repo1 rollout 01 promptIndex = %d, want 1", rollout.PromptIndex)
			}
			if rollout.Score != "7" {
				t.Fatalf("repo1 rollout 01 score = %q, want 7", rollout.Score)
			}
			if rollout.Reason != "repo1 reason 01" {
				t.Fatalf("repo1 rollout 01 reason = %q", rollout.Reason)
			}
		case "session-repo1-06":
			foundRepo1SecondPrompt = true
			if rollout.PromptIndex != 2 {
				t.Fatalf("repo1 rollout 06 promptIndex = %d, want 2", rollout.PromptIndex)
			}
		case "session-repo2-01":
			foundRepo2 = true
			if rollout.PromptIndex != 1 {
				t.Fatalf("repo2 rollout 01 promptIndex = %d, want 1", rollout.PromptIndex)
			}
			if filepath.Clean(rollout.GroupPath) != filepath.Clean(repo2) {
				t.Fatalf("repo2 rollout groupPath = %q, want %q", rollout.GroupPath, repo2)
			}
		}
	}

	if !foundRepo1First || !foundRepo1SecondPrompt || !foundRepo2 {
		t.Fatalf("expected three rollout groups to be parsed independently, got %#v", rollouts)
	}
}

func TestParseAuthLoginStartAcceptsNoWaitResponse(t *testing.T) {
	deviceCode, verificationURL, err := parseAuthLoginStart([]byte(`{"device_code":"dev-123","verification_url":"https://example.com/verify"}`))
	if err != nil {
		t.Fatalf("parseAuthLoginStart returned error: %v", err)
	}
	if deviceCode != "dev-123" {
		t.Fatalf("deviceCode = %q, want %q", deviceCode, "dev-123")
	}
	if verificationURL != "https://example.com/verify" {
		t.Fatalf("verificationURL = %q, want %q", verificationURL, "https://example.com/verify")
	}
}

func TestUpsertRecordResponseUsesNestedRecordIDList(t *testing.T) {
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
	out := []byte(`{"ok":true,"data":{"record":{"record_id_list":["rec123"]}}}`)
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if len(result.Data.Record.RecordIDList) == 0 || result.Data.Record.RecordIDList[0] != "rec123" {
		t.Fatalf("nested record_id_list not parsed: %#v", result.Data.Record.RecordIDList)
	}
}

func TestParseAuthLoginStartReturnsStructuredError(t *testing.T) {
	_, _, err := parseAuthLoginStart([]byte(`{"ok":false,"error":{"message":"scope missing"}}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "scope missing" {
		t.Fatalf("error = %q, want %q", err.Error(), "scope missing")
	}
}

func TestParseAuthLoginCompleteSuccess(t *testing.T) {
	authed, message, err := parseAuthLoginComplete([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("parseAuthLoginComplete returned error: %v", err)
	}
	if !authed {
		t.Fatal("authed = false, want true")
	}
	if message != "" {
		t.Fatalf("message = %q, want empty", message)
	}
}

func TestParseAuthLoginCompleteFallbackMessage(t *testing.T) {
	authed, message, err := parseAuthLoginComplete([]byte(`{"ok":false}`))
	if err != nil {
		t.Fatalf("parseAuthLoginComplete returned error: %v", err)
	}
	if authed {
		t.Fatal("authed = true, want false")
	}
	if message != "授权尚未完成，请确认已在飞书中完成扫码并同意授权后重试" {
		t.Fatalf("message = %q", message)
	}
}

func TestParseAuthLoginCompleteStructuredError(t *testing.T) {
	authed, message, err := parseAuthLoginComplete([]byte(`{"ok":false,"error":{"message":"authorization failed: The device_code is invalid. Please restart the device authorization flow."}}`))
	if err != nil {
		t.Fatalf("parseAuthLoginComplete returned error: %v", err)
	}
	if authed {
		t.Fatal("authed = true, want false")
	}
	want := "authorization failed: The device_code is invalid. Please restart the device authorization flow."
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}

func TestJSONEnvelopeSkipsPrefixLogs(t *testing.T) {
	raw := []byte("Uploading attachment: 01.patch\n{\"ok\":true}\n")
	payload, err := jsonEnvelope(raw)
	if err != nil {
		t.Fatalf("jsonEnvelope returned error: %v", err)
	}
	if string(payload) != "{\"ok\":true}\n" {
		t.Fatalf("payload = %q", string(payload))
	}
}

func TestParseAuthLoginCompleteWithPrefixLogs(t *testing.T) {
	raw := []byte("等待用户授权...\n{\n  \"ok\": true\n}\n")
	authed, message, err := parseAuthLoginComplete(raw)
	if err != nil {
		t.Fatalf("parseAuthLoginComplete returned error: %v", err)
	}
	if !authed {
		t.Fatal("authed = false, want true")
	}
	if message != "" {
		t.Fatalf("message = %q, want empty", message)
	}
}

func TestContainsQualifiedFalse(t *testing.T) {
	if !containsQualifiedFalse(`{"qualified": false, "score": 1}`) {
		t.Fatal("expected qualified=false to be detected")
	}
	if containsQualifiedFalse(`{"qualified": true}`) {
		t.Fatal("did not expect qualified=true to trigger")
	}
}

func TestContainsScoreIssue(t *testing.T) {
	if !containsScoreIssue("结论：不合理，分数偏高") {
		t.Fatal("expected score issue to be detected")
	}
	if containsScoreIssue("结论：合理") {
		t.Fatal("did not expect normal text to trigger")
	}
}

func TestCollectImportWarnings(t *testing.T) {
	fields := []TableField{
		{ID: fieldPromptIndex, Name: "Prompt"},
		{ID: fieldModelName, Name: "model_name"},
		{ID: "fld_prompt_qa", Name: "prompt质检分数及意见"},
		{ID: "fld_score_qa", Name: "score质检及意见"},
	}
	promptSnapshots := []RecordSnapshot{
		{RecordID: "rec_prompt_1", Values: map[string]string{fieldPromptIndex: "1", "fld_prompt_qa": `{"qualified": false}`}},
	}
	rolloutSnapshotsByPrompt := map[string][]RecordSnapshot{
		"rec_prompt_1": {
			{RecordID: "rec_rollout_1", Values: map[string]string{fieldRolloutID: "01", fieldModelName: "Doubao-Seed-2.0-Code", "fld_prompt_qa": `qualified=false`, "fld_score_qa": "判定不合理"}},
			{RecordID: "rec_rollout_2", Values: map[string]string{fieldRolloutID: "02", fieldModelName: "doubao-seed-2.0-code"}},
		},
	}

	warnings := collectImportWarnings(fields, promptSnapshots, rolloutSnapshotsByPrompt)
	if len(warnings) != 4 {
		t.Fatalf("len(warnings) = %d, want 4, warnings=%#v", len(warnings), warnings)
	}

	joined := ""
	for _, warning := range warnings {
		joined += warning.Scope + ":" + warning.Message + "\n"
	}
	for _, want := range []string{"Prompt 1 的 prompt质检分数及意见中 qualified=false", "Prompt 1 / Rollout 01 的 prompt质检分数及意见中 qualified=false", "Prompt 1 / Rollout 01 的 score质检及意见出现不合理", "Prompt 1 下存在重复 model_name：Doubao-Seed-2.0-Code（2 次）"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings missing %q in %q", want, joined)
		}
	}
}
