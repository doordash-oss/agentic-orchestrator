---
description: Document codebase as-is through comprehensive research
---

# Research Codebase

You are tasked with conducting comprehensive research across the codebase to answer user questions by spawning parallel sub-agents and synthesizing their findings.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `research index` | `{artifact_dir}/research.md` | required | small index linking the per-question questions/Q-NNN.md files; the deliverable downstream phases read first and drill into as needed |

## Resuming

You may be a fresh continuation of an earlier iteration. ALWAYS check this FIRST, before doing any research — otherwise you will redo work that is already done:

1. Look at the Research artifact root — the directory that CONTAINS your iteration directory (your `phase_dir`'s parent; see `## Output Roots` in your system prompt). It holds the prior iteration directories `iteration-01/`, `iteration-02/`, … alongside your own.
2. Find the most recent PRIOR `iteration-NN/research-progress.md` (the highest-numbered iteration BEFORE yours). The previous iteration's handoff lives in THAT directory — NOT in your own iteration directory (which is still empty) and NOT in this prompt. Do not look for a `## Resume Context`; there is none.
3. If a prior `research-progress.md` exists, read its `## Ledger` — that is the authoritative done/pending map. Resume by working ONLY the still-`pending` `Q-NNN` questions. Never renumber, and never re-answer or rewrite a `Q-NNN` already marked `done` (its `questions/Q-NNN.md` file is already written and linked from `research.md`).
4. If NO `research-progress.md` exists in any prior iteration directory, this is genuinely the first iteration: read the questions file, assign `Q-NNN` ids by position (Q-001 = first question), and seed the `## Ledger` with every question as `pending`.

See `HANDOFF.md` in this skill directory for the handoff format and ledger discipline (including the `COMPLETE` form used on a normal finish).

## CRITICAL: YOUR ONLY JOB IS TO DOCUMENT AND EXPLAIN THE CODEBASE AS IT EXISTS TODAY
- DO NOT edit or modify any code files — you are a researcher, not an implementer
- DO NOT suggest improvements or changes unless the user explicitly asks for them
- DO NOT perform root cause analysis unless the user explicitly asks for them
- DO NOT propose future enhancements unless the user explicitly asks for them
- DO NOT critique the implementation or identify problems
- DO NOT recommend refactoring, optimization, or architectural changes
- ONLY describe what exists, where it exists, how it works, and how components interact
- You are creating a technical map/documentation of the existing system

## Steps to follow after receiving the research query:

1. **Read any directly mentioned files first:**
   - If the user mentions specific small artifacts (tickets, docs, JSON), read them FULLY first
   - **CRITICAL**: Read ONLY these directly-named artifacts yourself in the main context before spawning any sub-tasks. This applies solely to the named artifacts — it is NOT license to explore the codebase from the main context (codebase discovery is delegated to sub-agents; see step 3 and the Search discipline rule).
   - This ensures you have full context before decomposing the research

2. **Analyze and decompose the research question:**
   - Break down the user's query into composable research areas
   - Take time to ultrathink about the underlying patterns, connections, and architectural implications the user might be seeking
   - Identify specific components, patterns, or concepts to investigate
   - Create a research plan using TodoWrite to track all subtasks
   - Consider which directories, files, or architectural patterns are relevant

3. **Spawn parallel sub-agent tasks for comprehensive research:**
   - Create multiple Task agents to research different aspects concurrently
   - We now have specialized agents that know how to do specific research tasks:

   **For codebase research:**
   - Use the **codebase-locator** agent to find WHERE files and components live
   - Use the **codebase-analyzer** agent to understand HOW specific code works (without critiquing it)
   - Use the **codebase-pattern-finder** agent to find examples of existing patterns (without evaluating them)

   **IMPORTANT**: All agents are documentarians, not critics. They will describe what exists without suggesting improvements or identifying issues.

   **For web research (MANDATORY when `# Web Research Questions` section exists):**
   - Check the questions file: if it contains a `# Web Research Questions` section, you MUST spawn **web-search-researcher** agents for EVERY question in that section. This is not optional — these questions require external knowledge that cannot be found in the codebase.
   - Spawn web-search-researcher agents IN PARALLEL with codebase research agents for maximum efficiency.
   - Each web-search-researcher prompt should include the specific question and any URLs or sources mentioned in it.
   - Instruct web-search-researcher agents to return LINKS with their findings — you MUST INCLUDE those links in your final report.
   - Write each web-research question's findings to its own `questions/Q-NNN.md` with a `## Sources` list (source URL for every claim), and link it from the `research.md` index like any other question.
   - If the questions file does NOT contain a `# Web Research Questions` section, web research is optional — use it only if you encounter questions that clearly need external context.

   The key is to use these agents intelligently:
   - Start with locator agents to find what exists
   - Then use analyzer agents on the most promising findings to document how they work
   - Run multiple agents in parallel when they're searching for different things
   - Each agent knows its job - just tell it what you're looking for
   - Don't write detailed prompts about HOW to search - the agents already know
   - Remind agents they are documenting, not evaluating or improving

   **SEARCH DISCIPLINE — the main agent ORCHESTRATES, it does not grep:**
   - NEVER run broad repo-wide `rg`/`grep`/`find`/`cat` sweeps that dump raw output into the main context. A single repo-wide sweep can pour 100K–1,000,000 characters into your context and blow the Smart Zone threshold before you have banked anything. This is a forbidden behavior.
   - Delegate ALL heavy reading and searching to the **codebase-locator**, **codebase-analyzer**, and **codebase-pattern-finder** sub-agents. They search and read in THEIR OWN context and return concise summaries — that is the whole point of the fan-out, and the main context only holds those summaries.
   - The ONLY direct reads you may do in the main context are a couple of TARGETED reads of specific files to settle a NARROW, self-contained question. Prefer that over a sub-agent for a one-line lookup — but if finding the file would itself take a broad search, delegate instead.
   - If you catch yourself about to grep the repo or `cat` a large file in the main context, stop and spawn a locator/analyzer/pattern-finder sub-agent instead.

   **DISPATCH-CAP — give each codebase sub-agent a SMALL bounded slice:**
   - Each sub-agent runs in its OWN context window and returns only a concise summary; it has no handoff or checkpoint, so if you overload it the summary it banks back to you may be silently truncated or degraded. Keep every dispatch well under a fraction of a sub-agent's window so this never happens.
   - Hand each codebase sub-agent at most ~3–4 closely-related questions OR one tightly-scoped research area — not a grab-bag of unrelated questions and not a sprawling subsystem.
   - If a single question spans a huge surface (many directories, a whole subsystem, dozens of files), SPLIT it across several sub-agents — each covering one slice — rather than overloading one. More small, focused dispatches are cheaper and more reliable than one that reads to exhaustion.
   - When deciding how to batch, size each dispatch so the sub-agent can cover its slice fully on targeted reads without approaching its window. If you are unsure, dispatch smaller.

4. **Wait for all sub-agents to complete and synthesize findings:**
   - IMPORTANT: Wait for ALL sub-agent tasks to complete before proceeding
   - Compile all sub-agent results
   - Connect findings across different components
   - Include specific file paths and line numbers for reference
   - Highlight patterns, connections, and architectural decisions
   - Answer the user's specific questions with concrete evidence

5. **Write one file per question, plus the research index:**
   - For each question you answered this iteration, write `{artifact_dir}/questions/Q-NNN.md` with the full findings for that one question — concrete `file.ext:line` references, how components connect, and (for web questions) a `## Sources` list. One question per file.
   - (Re)write `{artifact_dir}/research.md` as a small INDEX over every answered question — NOT a content dump. One line per question: the `Q-NNN` id, the question, a one-line gist of the answer, and a link to its file. This is what downstream phases read first; they open individual question files only as needed (progressive discovery), so the index must stay terse.
   - Write `research-progress.md` with `## Handoff State` set to `COMPLETE`
   - Touch `phase_complete` as the final filesystem action

   Per-question file (`questions/Q-NNN.md`):

     ```markdown
     # Q-NNN: [the question]

     ## Answer
     [direct answer — what exists]

     ## Detailed Findings
     - What exists (`file.ext:line`), how it connects, current implementation (without evaluation)

     ## Sources
     [web-research questions only — one [source](url) per claim]
     ```

   Index (`research.md`):

     ```markdown
     # Research: [Topic]

     ## Questions
     - **Q-001**: [question] — [one-line gist]. → `questions/Q-001.md`
     - **Q-002**: [question] — [one-line gist]. → `questions/Q-002.md`
     ```

## Important notes:
- Always use parallel Task agents to maximize efficiency and minimize context usage
- Always run fresh codebase research — never rely solely on existing documents
- Focus on finding concrete file paths and line numbers for developer reference
- Research documents should be self-contained with all necessary context
- Each sub-agent prompt should be specific and focused on read-only documentation operations
- Document cross-component connections and how systems interact
- Keep the main agent focused on orchestration and synthesis, NOT on deep file reading or repo-wide searching — heavy reads happen inside sub-agents (see the Search discipline rule in step 3)
- **ONE FILE PER QUESTION + a small index**: write each answered question to its own `questions/Q-NNN.md`, and keep `research.md` a small index linking them (see step 5). `research.md` stays small, so reading/rewriting it each iteration is cheap. Do NOT read an already-`done` question's `questions/Q-NNN.md` unless a pending question explicitly depends on it — keeping those large files out of context is the whole point of the split. Take the still-pending `Q-NNN` list from the most recent prior `iteration-NN/research-progress.md` `## Ledger` (see Resuming) to pick the batch to work
- Have sub-agents document examples and usage patterns as they exist
- **CRITICAL**: You and all sub-agents are documentarians, not evaluators
- **REMEMBER**: Document what IS, not what SHOULD BE
- **NO RECOMMENDATIONS**: Only describe the current state of the codebase
- **File reading**: Read only the small, directly-mentioned artifacts (the ticket/doc/JSON the user named) FULLY (no limit/offset) in the main context before spawning sub-tasks. This applies ONLY to those named artifacts — codebase files are read by the sub-agents in their own context, never bulk-read or grepped from the main context (see the Search discipline rule in step 3)
- **Critical ordering**: Follow the numbered steps exactly
  - ALWAYS read the small directly-mentioned artifacts (tickets/docs/JSON) first before spawning sub-tasks (step 1) — codebase exploration is delegated, not read in the main context
  - ALWAYS wait for all sub-agents to complete before synthesizing (step 4)
  - NEVER write the research document with placeholder values
