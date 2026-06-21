# Daily Standup & Progress Tracker Skill

## 🎯 Skill Objective
Analyze the user's recent GitHub activity using the GitHub MCP server (or standard GitHub integration) to generate a comprehensive daily standup report. The report must summarize the last working day's accomplishments and outline prioritized tasks for today.

## 📅 Time Context & Work Detection Logic
Determine the current date and apply the following lookback logic to find the "Last Working Day":

1. **Standard Working Days**: Monday through Friday.
2. **Weekend Handling**: If today is Monday, your default lookback period is the previous Friday. However, you MUST explicitly check Saturday and Sunday. If any GitHub activity occurred on the weekend, include it as part of the summary.
3. **Absence & Leave Handling (Up to 5 Days)**:
   - Check the immediately preceding day for GitHub activity.
   - If no activity is found, incrementally look backward one day at a time.
   - Continue searching backward for a maximum of **5 days** to find the last work done.
   - The day(s) containing the last found activity will be treated as the "Last Working Day."

## 🛠️ Data Sources
Execute your repository/commit search tools (e.g., `search_commits`, `search_issues`, `search_pull_requests` via GitHub MCP) for the user to gather the activity within the determined timeframe.

## 📋 Output Format Requirements
Generate the response strictly using the following Markdown structure. 

### 1. Header Context
Always start with the current date and the date of the last detected activity.

```markdown
# 📈 Daily Progress Report - [Current Date]
*Tracking activity since: [Last Working Day Date(s)]*
```

### 2. Yesterday's Summary (Last Working Day)
Summarize what was accomplished based on actual commits, merged PRs, and closed issues.

```markdown
## ⏪ Last Working Day's Summary
* **[Repository Name]**: [Action taken - e.g., "Implemented distributed tracing with OpenTelemetry"]
* **[Repository Name]**: [Action taken - e.g., "Fixed bug in login workflow (#45)"]
```

### 3. Today's Tasks (Strictly Prioritized)
Based on open issues, ongoing PRs, roadmap files, or previous day's incomplete work, outline the tasks for today. **Crucially, these must be ordered by priority (Highest to Lowest).**

```markdown
## 🎯 Today's Tasks
1. 🔴 **[High Priority]** [Task Name]: [Brief, actionable description]
2. 🟡 **[Medium Priority]** [Task Name]: [Brief, actionable description]
3. 🟢 **[Low Priority]** [Task Name]: [Brief, actionable description]
```

## 🚨 Constraints & Rules
- Do NOT generate generic placeholder summaries. Base everything on real data retrieved from GitHub.
- If absolutely no work is found within the maximum 5-day lookback window, explicitly state: *"No activity detected in the past 5 days."*
- All tasks in the "Today's Tasks" section MUST be ranked by urgency/priority.
