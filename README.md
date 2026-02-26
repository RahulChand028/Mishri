# Mishri: The Sovereign Agentic Engine 🛰️

Mishri is a futuristic, high-performance AI agent framework designed for autonomous task orchestration, robust governance, and real-time observability. Built with a Master-Slave architecture, it excels at decomposing complex goals into actionable steps and executing them with precision.

![Cyber-Terminal Mockup](screenshots/cyber_terminal_v5.png)
*(Note: Visual representation of the Cyber-Terminal UI)*

## 🌌 Features

### 🖥️ Cyber-Terminal UI
A high-tech, real-time command-line interface featuring:
- **Dynamic Dashboard**: A fixed-position header area displaying role (Master/Slave), active tasks, and animated status indicators.
- **Visual Pulse Monitor**: A real-time system vital indicator (🟢/🟡/🔴) that replaces raw logs with visual health status.
- **Live Resource Tracking**: Real-time monitoring of CPU, memory, and uptime with an integrated progress bar.
- **Minimalist Aesthetics**: A clean, distraction-free startup sequence with neon-themed Unicode framing.

### 🧠 Master-Slave Orchestration
- **MasterBrain**: Acts as the architect, using advanced planning tools to decompose user requests into a structured task tree.
- **WorkerBrain**: A dedicated execution agent that handles specialized tools while being governed by strict security policies.

### 🛡️ Governance & Security
- **Policy Engine**: A rule-based system that monitors and filters tool execution, protecting the host system from dangerous commands.
- **Tool Guarding**: Regex-based argument filtering and role-based access control for all integrated tools.

### 📊 Observability & Resilience
- **Persistent History**: Full task trees and execution logs stored in a robust SQLite backend.
- **Heartbeat Resilience**: Background monitors ensure all gateways and schedulers are alive and reporting.
- **Graceful Shutdown**: Native OS signal handling (`SIGINT`/`SIGTERM`) for safe resource cleanup.

## 🛠️ Tech Stack

- **Language**: Go (Golang)
- **Framework**: [LangChainGo](https://github.com/tmc/langchaingo)
- **Gateways**: Telegram, CLI
- **Persistence**: SQLite (via `modernc.org/sqlite`)
- **CLI**: ANSI Escape Codes, `golang.org/x/term`

## 🚀 Getting Started

### Prerequisites
- Go 1.21+
- OpenAI API Key (or compatible LLM provider)

### Installation
1. Clone the repository:
   ```bash
   git clone https://github.com/rahul/mishri.git
   cd mishri
   ```
2. Configure your environment:
   ```bash
   cp .env.example .env
   cp config.example.json config.json
   # Edit config.json with your API keys and workspace path
   ```
3. Build the engine:
   ```bash
   make build
   ```

### Usage
Run the CLI dashboard:
```bash
./bin/mishri
```

## 🗺️ Roadmap
Check out the [ROADMAP.md](ROADMAP.md) for planned features including:
- Human-in-the-Loop (HITL) approval gates.
- Cost and Token usage tracking.
- Tool Call Trace Visualization.

## ⚖️ License
This project is licensed under the MIT License - see the LICENSE file for details.
