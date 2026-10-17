package main

// One shape for every prompt Lucille hands to another AI (user 2026-10-15: "Lucille needs to make a
// formal prompt to any AI she talks to"). The coder mailbox (jobs.go), the ready-to-run `claude`
// commands in the project briefs (projectbrief.go) and the phone's "Copy for an AI" all use these
// sections, in this order, so a coder can always find the job, the limits and how to finish:
//
//   WHO / THE JOB / BACKGROUND / WHAT HAS BEEN SAID / READ FIRST / WHERE / HOW TO WORK / DONE WHEN / REPORT BACK
//
// The phone's copy of the shape lives in LucilleApi.kt (briefingText); keep the headings equal.

import (
	"fmt"
	"strings"
)

type formalPrompt struct {
	Who        string   // who is asking, on whose behalf, and who the reader is
	Job        string   // what to do, in the asker's words
	Background string   // material the job refers to (a briefing, a statement), quoted as-is
	Said       []string // the conversation so far, one line each
	ReadFirst  []string // files to read before anything else
	Project    string   // project code or name
	Dir        string   // working directory
	Rules      []string // numbered under HOW TO WORK
	DoneWhen   string
	Report     string // how to report back (a command, or what to say)
}

// Rules every coder gets, whoever starts it.
var baseRules = []string{
	"Read /mnt/recoverybuild/00-CURRENT/AI_INSTRUCTIONS.md and find this item in /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md. Claim the area on the ai-board before you edit anything, and stay out of areas another AI has claimed.",
	"Do not touch the user's keys, other drives, or phones. Anything destructive needs the user's explicit OK first.",
}

func (p formalPrompt) sections() [][2]string {
	var s [][2]string
	add := func(h, body string) {
		if strings.TrimSpace(body) != "" {
			s = append(s, [2]string{h, body})
		}
	}
	add("WHO", p.Who)
	add("THE JOB", p.Job)
	add("BACKGROUND", p.Background)
	add("WHAT HAS BEEN SAID", bullets(p.Said))
	add("READ FIRST", bullets(p.ReadFirst))
	switch {
	case p.Project != "" && p.Dir != "":
		add("WHERE", fmt.Sprintf("project %s, working directory %s", p.Project, p.Dir))
	case p.Dir != "":
		add("WHERE", "working directory "+p.Dir)
	}
	var rules strings.Builder
	for i, r := range p.Rules {
		fmt.Fprintf(&rules, "%d. %s\n", i+1, r)
	}
	add("HOW TO WORK", strings.TrimRight(rules.String(), "\n"))
	add("DONE WHEN", p.DoneWhen)
	add("REPORT BACK", p.Report)
	return s
}

// Text is the prompt as a document: headings on their own lines.
func (p formalPrompt) Text() string {
	var b strings.Builder
	for i, s := range p.sections() {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s:\n%s\n", s[0], s[1])
	}
	return b.String()
}

// OneLine is the same prompt on a single line, for a shell command a person pastes into a terminal.
func (p formalPrompt) OneLine(n int) string {
	var parts []string
	for _, s := range p.sections() {
		parts = append(parts, s[0]+": "+strings.Join(strings.Fields(s[1]), " "))
	}
	return oneLine(strings.Join(parts, " | "), n)
}

func bullets(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
