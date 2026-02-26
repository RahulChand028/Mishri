# Mishri Project Roadmap

This roadmap outlines the planned development for Mishri, focusing on building a robust, observable, and governed AI agent framework.

## Phase 1: Governance & Security
Establish a strong foundation for tool execution and agent behavior.

- **Policy Engine**: Implement a rule-based system to define what actions the agent can take based on context, user, and tool type.
- **Guarded Tool Runtime**: Create a secure execution environment for tools, including:
    - Resource limits (memory, time).
    - Permission-based tool access.
    - Input validation and output sanitization.

## Phase 2: Observability & Monitoring
Gain deep insight into the agent's internal reasoning and tool interactions.

- **Structured Event Logs**: Move from simple text logs to structured JSON events for easier parsing and analysis.
- **Task Execution History**: Persistent storage and retrieval of full task trees, including sub-tasks and their results.
- **Tool Call Trace Graph**: Visualize the sequence and relationship of tool calls to debug complex orchestrations.
- **Cost Tracking**: Monitor token usage and API costs in real-time at the task and session level.

## Phase 3: Runtime Control & Resilience
Improve the system's ability to handle failures and maintain stability.

- **Failure Retry Strategy**: Implement intelligent retries for tool calls and LLM requests with exponential backoff.
- **Deadlock Detection**: Monitor orchestration loops and stuck states in the Master-Slave communication.
- **Heartbeat Monitor**: Ensure background processes (like the Scheduler and long-running tools) are alive and responsive.
- **Task Preemption & Cancellation**: Allow users to safely stop or pivot the agent during complex executions.

## Phase 4: Advanced Hardening & Performance
Level up the system for production-grade reliability and security.

- **Graceful Shutdown**: Implement OS signal handling (`SIGINT`/`SIGTERM`) to ensuring background tasks and database connections close safely.
- **Advanced Policy Rules**: Expand the `PolicyEngine` to support:
    - Regex-based argument filtering (e.g., block `rm` in shell).
    - Rate limiting per tool/user.
- **Performance Optimization**: Add database indexing to `HistoryStore` for faster lookups as logs grow.
- **Human-in-the-Loop (HITL)**: Implement a mechanism to pause execution and request user approval for high-risk tool calls.

---
*Note: This is a living document and will be updated as the project evolves.*
