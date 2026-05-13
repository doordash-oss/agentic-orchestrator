---
description: Document codebase as-is through comprehensive research
---

# Research Codebase

You are tasked with conducting comprehensive research across the codebase to answer user questions by spawning parallel sub-agents and synthesizing their findings.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `research markdown artifact` | `{phase_dir}/<newest non-excluded *.md>` | required | newest non-excluded markdown artifact in the phase directory |

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
   - If the user mentions specific files (tickets, docs, JSON), read them FULLY first
   - **CRITICAL**: Read these files yourself in the main context before spawning any sub-tasks
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
   - Synthesize web research findings into a dedicated `## Web Research Findings` section in your output document, with source URLs for every claim.
   - If the questions file does NOT contain a `# Web Research Questions` section, web research is optional — use it only if you encounter questions that clearly need external context.

   The key is to use these agents intelligently:
   - Start with locator agents to find what exists
   - Then use analyzer agents on the most promising findings to document how they work
   - Run multiple agents in parallel when they're searching for different things
   - Each agent knows its job - just tell it what you're looking for
   - Don't write detailed prompts about HOW to search - the agents already know
   - Remind agents they are documenting, not evaluating or improving

4. **Wait for all sub-agents to complete and synthesize findings:**
   - IMPORTANT: Wait for ALL sub-agent tasks to complete before proceeding
   - Compile all sub-agent results
   - Connect findings across different components
   - Include specific file paths and line numbers for reference
   - Highlight patterns, connections, and architectural decisions
   - Answer the user's specific questions with concrete evidence

5. **Write the research document to the output directory:**
   - Name it `YYYY-MM-DD-research.md` (e.g., `2025-01-08-research.md`)
   - Structure the document as follows:

     ```markdown
     # Research: [Topic]

     ## Research Question
     [Original user query or questions from the Inquire phase]

     ## Summary
     [High-level documentation of what was found, answering the question by describing what exists]

     ## Detailed Findings

     ### [Component/Area 1]
     - Description of what exists (`file.ext:line`)
     - How it connects to other components
     - Current implementation details (without evaluation)

     ### [Component/Area 2]
     ...

     ## Web Research Findings
     [Include this section ONLY if web research questions were answered.
      Organize by topic with source URLs for every claim.]

     ### [Topic 1]
     - Finding with [source](url)
     - Finding with [source](url)

     ### [Topic 2]
     ...

     ## Code References
     - `path/to/file.py:123` - Description of what's there
     - `another/file.ts:45-67` - Description of the code block

     ## Architecture Documentation
     [Current patterns, conventions, and design implementations found in the codebase]

     ## Open Questions
     [Any areas that need further investigation]
     ```

## Important notes:
- Always use parallel Task agents to maximize efficiency and minimize context usage
- Always run fresh codebase research — never rely solely on existing documents
- Focus on finding concrete file paths and line numbers for developer reference
- Research documents should be self-contained with all necessary context
- Each sub-agent prompt should be specific and focused on read-only documentation operations
- Document cross-component connections and how systems interact
- Keep the main agent focused on synthesis, not deep file reading
- Have sub-agents document examples and usage patterns as they exist
- **CRITICAL**: You and all sub-agents are documentarians, not evaluators
- **REMEMBER**: Document what IS, not what SHOULD BE
- **NO RECOMMENDATIONS**: Only describe the current state of the codebase
- **File reading**: Always read mentioned files FULLY (no limit/offset) before spawning sub-tasks
- **Critical ordering**: Follow the numbered steps exactly
  - ALWAYS read mentioned files first before spawning sub-tasks (step 1)
  - ALWAYS wait for all sub-agents to complete before synthesizing (step 4)
  - NEVER write the research document with placeholder values
