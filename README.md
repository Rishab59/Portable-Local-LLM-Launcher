# PortableAI — Portable Local AI Environment

## 🧠 Project Overview
PortableAI is a **Windows-first** portable environment for running selected local AI models from a USB drive or local folder. It provides a lightweight controller/dashboard and local model runtime, enabling users to launch and manage AI models without system-wide installation or dependencies.

---

## ✅ Key Features
- **Portable & USB-friendly** — No system-wide dependencies required
- **Local AI model launcher** — Users manually download and place model/runtime files
- **Lightweight controller** — Dashboard at `http://127.0.0.1:8787` with API key management *(Port can be configured)*
- **Model server** — Local inference API at `http://127.0.0.1:8080` *(Port can be configured)*
- **Multi-modal support** — Tested with text, vision, and embedding models
- **CPU/GPU inference** — Works with AMD Radeon and NVIDIA GPUs
- **Offline operation** — No internet required after initial setup

---

## 📦 Folder Structure
PortableAI/
├── Config/
│ ├── controller.json
│ ├── model-catalog.json
│ └── settings.json
├── Controller/
│ ├── source
│ └── windows-x64/
│ └── portableai-controller.exe
├── ControlPlane/
├── Docs/
├── Licenses/
├── Logs/
├── Models/
│ ├── vision/
│ ├── reasoning/
│ ├── coding/
│ ├── embedding/
│ └── general/
├── Runtime/
│ └── windows-x64/
│  └── llamafile-0.10.6.exe
├── Start-PortableAI.bat
├── Start-PortableAI.ps1
├── Stop-PortableAI.bat
└── Stop-PortableAI.ps1


> Note: Model files (`.gguf`, `.safetensors`, etc.) are **excluded from the GitHub repository** and must be manually downloaded and placed in the `Models/` directory as nneded.

---

## 🧾 Tested Models
| Category       | Model Name               | Notes                                                                 |
|----------------|--------------------------|-----------------------------------------------------------------------|
| General        | Qwen3-8B                | Confirmed to respond                       |
| Coding         | Qwen2.5-Coder-3B-Instruct | Confirmed to respond                                                 |
| Vision         | Qwen3-VL-2B-Instruct     | Works with 8192 context setting (4096 context overflow reported)    |
| Embedding      | Qwen3-Embedding-0.6B     | Standalone test returns 1024-dimensional embeddings (controller integration pending) |

> All models require manual download and placement in the `Models/` directory.

---

## 📌 How to Use
1. **Download models** and place them in the appropriate `Models/` subfolder as needed
2. **Run `Start-PortableAI.bat`** to launch the controller/dashboard
3. **Copy API key** from Dashboard/RunningServers and paste it into the model web interface
4. **Use the model server** at `http://127.0.0.1:8080` for inference *(8080 is default port, can be configured in Dashboard while launching the model)*
5. **Run `Stop-PortableAI.bat`** to gracefully shut down the environment 

> *Note: Terminating the controller will also stop all the Running models*
> Refer Initial Setup tab in Dashboard for detailed steps

---

## ⚠️ Limitations & Deferred Features
- **Not fully portable** — Requires manual setup of model files and Runtime
- **Embedding integration** — Standalone test confirmed, controller integration pending
- **No license verification** — Users must verify upstream licenses for all models

---

## 📚 Security & Licensing
- **Local-only operation** — Controller binds to `127.0.0.1` by default
- **API key management** — Dashboard includes show/hide and copy functionality
- **No model weights in repo** — All weights are excluded via `.gitignore`
- **License compliance** — Users must verify individual model licenses

---

## 📦 GitHub Setup
The project is being prepared for GitHub release. The repository will include:
- Controller binaries and scripts
- Configuration files
- Folder structure with `.keep` placeholders for model directories
- `.gitignore` to exclude model files

> Model files must be manually downloaded and placed in the `Models/` directory before use.

---

## 🛠️ Technical Notes
- **PowerShell/Windows integration** — Batch scripts invoke PowerShell scripts to avoid quoting issues
- **Default Model server ports**:
  - Controller: `http://127.0.0.1:8787`
  - Model inference: `http://127.0.0.1:8080`
- **Runtime** — Mozilla AI `llamafile-0.10.6.exe` for GGUF models

> Runtime file has to be downloaded seperately from [Mozilla AI llamafile releases](https://github.com/Mozilla-Ocho/llamafile/releases), it will not be present in releases

---

## 📌 Disclaimer
This is an early **Windows-first v1** release. Features like audio/image generation and full model catalog integration are deferred. The project is not production-ready *(just a hobby project)* and requires manual setup of model files & Runtime.
