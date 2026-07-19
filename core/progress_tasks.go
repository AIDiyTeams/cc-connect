package core

import "strings"

type ProgressTaskStatus string

const (
	ProgressTaskPending    ProgressTaskStatus = "pending"
	ProgressTaskInProgress ProgressTaskStatus = "in_progress"
	ProgressTaskCompleted  ProgressTaskStatus = "completed"
	ProgressTaskFailed     ProgressTaskStatus = "failed"
)

// ProgressTask is a user-facing, high-level stage of the current request.
// It intentionally excludes commands, paths, tool output, and chain-of-thought.
type ProgressTask struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Status ProgressTaskStatus `json:"status"`
}

type progressTaskKind string

const (
	progressTaskContext  progressTaskKind = "context"
	progressTaskResearch progressTaskKind = "research"
	progressTaskPlan     progressTaskKind = "plan"
	progressTaskAnalysis progressTaskKind = "analysis"
	progressTaskVisuals  progressTaskKind = "visuals"
	progressTaskContent  progressTaskKind = "content"
	progressTaskBuild    progressTaskKind = "build"
	progressTaskValidate progressTaskKind = "validate"
	progressTaskDeliver  progressTaskKind = "deliver"
	progressTaskPublish  progressTaskKind = "publish"
)

type progressTaskProfile struct {
	request       string
	social        bool
	xiaohongshu   bool
	visuals       bool
	content       bool
	research      bool
	analysis      bool
	build         bool
	publish       bool
	completeAsset bool
}

type progressTaskTracker struct {
	lang         Language
	profile      progressTaskProfile
	tasks        []ProgressTask
	kinds        map[string]progressTaskKind
	lastToolKind progressTaskKind
}

func newProgressTaskTracker(rawRequest string, lang Language) *progressTaskTracker {
	request := extractProgressUserRequest(rawRequest)
	profile := buildProgressTaskProfile(request)
	kinds := buildInitialProgressTaskKinds(profile)
	t := &progressTaskTracker{
		lang:    lang,
		profile: profile,
		kinds:   make(map[string]progressTaskKind, len(kinds)),
	}
	for i, kind := range kinds {
		task := ProgressTask{
			ID:     string(kind),
			Title:  progressTaskTitle(profile, kind, lang),
			Status: ProgressTaskPending,
		}
		if i == 0 {
			task.Status = ProgressTaskInProgress
		}
		t.tasks = append(t.tasks, task)
		t.kinds[task.ID] = kind
	}
	return t
}

func (t *progressTaskTracker) Tasks() []ProgressTask {
	if t == nil {
		return nil
	}
	out := make([]ProgressTask, len(t.tasks))
	copy(out, t.tasks)
	return out
}

// Observe uses raw agent events only as private routing signals. Raw event text
// is never copied into user-facing task titles.
func (t *progressTaskTracker) Observe(item ProgressCardEntry) []ProgressTask {
	if t == nil {
		return nil
	}
	kind := progressTaskKind("")
	if item.Kind == ProgressEntryToolResult && t.lastToolKind != "" {
		kind = t.lastToolKind
	} else {
		kind = classifyProgressTaskEvent(item)
	}
	if item.Kind == ProgressEntryToolUse && kind != "" {
		t.lastToolKind = kind
	}
	if kind != "" {
		t.activate(kind)
	}
	if item.Kind == ProgressEntryError {
		for i := range t.tasks {
			if t.tasks[i].Status == ProgressTaskInProgress {
				t.tasks[i].Status = ProgressTaskFailed
				break
			}
		}
	}
	return t.Tasks()
}

func (t *progressTaskTracker) Finalize(state ProgressCardState) []ProgressTask {
	if t == nil {
		return nil
	}
	if state == ProgressCardStateFailed {
		for i := range t.tasks {
			if t.tasks[i].Status == ProgressTaskInProgress {
				t.tasks[i].Status = ProgressTaskFailed
				return t.Tasks()
			}
		}
		return t.Tasks()
	}
	for i := range t.tasks {
		if t.tasks[i].Status != ProgressTaskFailed {
			t.tasks[i].Status = ProgressTaskCompleted
		}
	}
	return t.Tasks()
}

func (t *progressTaskTracker) activate(kind progressTaskKind) {
	if kind == "" {
		return
	}
	target := -1
	for i := range t.tasks {
		if t.kinds[t.tasks[i].ID] == kind {
			target = i
			break
		}
	}
	if target >= 0 && (t.tasks[target].Status == ProgressTaskCompleted || t.tasks[target].Status == ProgressTaskFailed) {
		return
	}
	for i := range t.tasks {
		if t.tasks[i].Status == ProgressTaskInProgress {
			if i == target {
				return
			}
			t.tasks[i].Status = ProgressTaskCompleted
			break
		}
	}

	var next ProgressTask
	if target >= 0 {
		next = t.tasks[target]
		t.tasks = append(t.tasks[:target], t.tasks[target+1:]...)
	} else {
		if len(t.tasks) >= 5 || !isHighValueProgressTask(kind) {
			return
		}
		next = ProgressTask{
			ID:    string(kind),
			Title: progressTaskTitle(t.profile, kind, t.lang),
		}
		t.kinds[next.ID] = kind
	}
	next.Status = ProgressTaskInProgress

	// Keep completed work first, the live stage second, and the remaining plan
	// pending. This lets the plan adjust to the Agent's actual execution order.
	insertAt := 0
	for insertAt < len(t.tasks) && t.tasks[insertAt].Status == ProgressTaskCompleted {
		insertAt++
	}
	t.tasks = append(t.tasks, ProgressTask{})
	copy(t.tasks[insertAt+1:], t.tasks[insertAt:])
	t.tasks[insertAt] = next
}

func extractProgressUserRequest(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	for _, marker := range []string{"User request:", "User message:"} {
		if idx := strings.LastIndex(text, marker); idx >= 0 {
			text = strings.TrimSpace(text[idx+len(marker):])
			break
		}
	}
	for _, end := range []string{"\n\nWhen producing content:", "\n[Context Summary]", "\n[User Memory Index]"} {
		if idx := strings.Index(text, end); idx >= 0 {
			text = strings.TrimSpace(text[:idx])
		}
	}
	if len([]rune(text)) > 1200 {
		text = string([]rune(text)[:1200])
	}
	return text
}

func buildProgressTaskProfile(request string) progressTaskProfile {
	q := strings.ToLower(request)
	profile := progressTaskProfile{request: request}
	profile.xiaohongshu = containsAny(q, "小红书", "xiaohongshu", "rednote", "red note")
	profile.social = profile.xiaohongshu || containsAny(q,
		"推文", "帖子", "社媒", "微博", "公众号", "linkedin", "twitter", "tweet", "reddit", "social post")
	profile.visuals = containsAny(q,
		"图片", "配图", "封面", "海报", "视觉", "插图", "图像", "image", "visual", "cover", "poster", "banner", "thumbnail")
	profile.content = containsAny(q,
		"文章", "文案", "配文", "标题", "正文", "标签", "脚本", "推文", "帖子", "article", "copy", "caption", "headline", "content", "script", "post")
	profile.research = containsAny(q,
		"调研", "研究", "查找", "搜索", "核对资料", "热点", "趋势", "竞品", "research", "search", "trend", "source", "competitor")
	profile.analysis = containsAny(q,
		"分析", "诊断", "审计", "比较", "评估", "复盘", "原因", "analyze", "analysis", "audit", "diagnose", "compare", "evaluate", "review")
	profile.build = containsAny(q,
		"实现", "开发", "修复", "写代码", "改代码", "搭建网站",
		"build", "implement", "develop", "fix bug", "write code", "codebase", "component")
	profile.publish = containsAny(q,
		"部署", "发布", "上线", "推送", "deploy", "publish", "release", "ship")
	profile.completeAsset = containsAny(q,
		"完整", "所有信息", "全部", "完备", "complete", "all information", "end-to-end")
	return profile
}

func buildInitialProgressTaskKinds(profile progressTaskProfile) []progressTaskKind {
	add := func(kinds []progressTaskKind, kind progressTaskKind) []progressTaskKind {
		for _, existing := range kinds {
			if existing == kind {
				return kinds
			}
		}
		if len(kinds) < 5 {
			return append(kinds, kind)
		}
		return kinds
	}

	kinds := []progressTaskKind{progressTaskContext}
	socialAsset := profile.social && (profile.content || profile.visuals)
	if socialAsset || (profile.content && profile.visuals) || profile.completeAsset {
		kinds = add(kinds, progressTaskPlan)
	} else if profile.research {
		kinds = add(kinds, progressTaskResearch)
	}
	if profile.analysis && !socialAsset {
		kinds = add(kinds, progressTaskAnalysis)
	}
	if profile.build {
		kinds = add(kinds, progressTaskBuild)
	}
	if profile.visuals {
		kinds = add(kinds, progressTaskVisuals)
	}
	if profile.content {
		kinds = add(kinds, progressTaskContent)
	}
	if profile.publish {
		kinds = add(kinds, progressTaskPublish)
	}
	if len(kinds) < 5 {
		if profile.visuals || profile.content || profile.build || profile.analysis || profile.research {
			kinds = add(kinds, progressTaskValidate)
		} else {
			kinds = add(kinds, progressTaskDeliver)
		}
	}
	return kinds
}

func progressTaskTitle(profile progressTaskProfile, kind progressTaskKind, lang Language) string {
	zh := lang == LangChinese || lang == LangTraditionalChinese
	if zh {
		if profile.xiaohongshu {
			switch kind {
			case progressTaskContext:
				return "梳理小红书主题与品牌信息"
			case progressTaskResearch:
				return "查找并核对热点方向"
			case progressTaskPlan:
				return "规划内容结构与表达重点"
			case progressTaskVisuals:
				return "生成并检查封面与配图"
			case progressTaskContent:
				return "撰写标题、正文与标签"
			case progressTaskValidate, progressTaskDeliver:
				return "整理并核对完整发布内容"
			}
		}
		switch kind {
		case progressTaskContext:
			return "梳理目标与相关信息"
		case progressTaskResearch:
			return "查找并核对关键信息"
		case progressTaskPlan:
			return "规划成果结构与推进方式"
		case progressTaskAnalysis:
			return "分析证据并形成判断"
		case progressTaskVisuals:
			return "生成并检查视觉素材"
		case progressTaskContent:
			return "撰写并完善核心内容"
		case progressTaskBuild:
			return "实现核心方案"
		case progressTaskValidate:
			return "检查结果是否符合要求"
		case progressTaskPublish:
			return "发布并确认运行状态"
		default:
			return "整理可直接使用的成果"
		}
	}

	if profile.xiaohongshu {
		switch kind {
		case progressTaskContext:
			return "Review the Xiaohongshu topic and brand context"
		case progressTaskResearch:
			return "Research and verify the trending angle"
		case progressTaskPlan:
			return "Plan the content structure and key message"
		case progressTaskVisuals:
			return "Create and review the cover and supporting visuals"
		case progressTaskContent:
			return "Write the title, body, and hashtags"
		case progressTaskValidate, progressTaskDeliver:
			return "Review and package the complete post"
		}
	}
	switch kind {
	case progressTaskContext:
		return "Review the goal and relevant context"
	case progressTaskResearch:
		return "Research and verify key information"
	case progressTaskPlan:
		return "Plan the deliverable and execution approach"
	case progressTaskAnalysis:
		return "Analyze the evidence and form a conclusion"
	case progressTaskVisuals:
		return "Create and review visual assets"
	case progressTaskContent:
		return "Draft and refine the core content"
	case progressTaskBuild:
		return "Implement the core solution"
	case progressTaskValidate:
		return "Verify the result against the requirements"
	case progressTaskPublish:
		return "Publish and verify the live result"
	default:
		return "Package the final deliverable"
	}
}

func classifyProgressTaskEvent(item ProgressCardEntry) progressTaskKind {
	q := strings.ToLower(strings.TrimSpace(item.Tool + " " + item.Text + " " + item.Status))
	if q == "" {
		return ""
	}
	if containsAny(q, "/api/image", "image", "figma", "canvas", "poster", "thumbnail", "cover", "视觉", "图片", "配图", "封面", "海报") {
		return progressTaskVisuals
	}
	if containsAny(q, "deploy", "publish", "release", "docker compose", "kubectl", "上线", "部署", "发布") {
		return progressTaskPublish
	}
	if containsAny(q, "test", "lint", "typecheck", "verify", "validation", "验收", "测试", "校验") {
		return progressTaskValidate
	}
	if containsAny(q, "browser", "web search", "websearch", "search_query", "research", "source", "competitor", "调研", "搜索", "查找", "资料来源", "竞品") {
		return progressTaskResearch
	}
	if containsAny(q, "compose", "draft", "write the", "headline", "caption", "article", "hashtags", "正文", "标题", "标签", "文案", "文章", "撰写") {
		return progressTaskContent
	}
	if containsAny(q, "apply_patch", "edit", "implement", "build", "compile", "code", "npm", "pnpm", "mvn", "开发", "实现", "修复", "代码") {
		return progressTaskBuild
	}
	if containsAny(q, "analyze", "diagnose", "audit", "compare", "evaluate", "分析", "诊断", "审计", "比较", "评估") {
		return progressTaskAnalysis
	}
	if containsAny(q, "plan", "outline", "structure", "规划", "大纲", "结构") {
		return progressTaskPlan
	}
	if containsAny(q, "cat ", "read", "grep", "rg ", "glob", "list", "memory", "context", "brand", "上下文", "品牌", "读取") {
		return progressTaskContext
	}
	return ""
}

func isHighValueProgressTask(kind progressTaskKind) bool {
	switch kind {
	case progressTaskResearch, progressTaskPlan, progressTaskAnalysis, progressTaskVisuals,
		progressTaskContent, progressTaskBuild, progressTaskValidate, progressTaskPublish:
		return true
	default:
		return false
	}
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
