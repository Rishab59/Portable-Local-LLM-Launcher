# Initial setup

PortableAI runs from this folder. It does not download models, runtimes, drivers, or dependencies. Keep this drive connected while using it.

## Folder layout

- `Controller/<platform>/`: prebuilt controller for the current operating system and CPU architecture.
- `Controller/source/`: Go source code used to build controller binaries.
- `Runtime/<platform>/`: manually supplied llamafile executable.
- `Models/`: manually supplied GGUF model and projector files.
- `Config/`: controller settings, model catalog, model registry, and per-model launch profiles.
- `Logs/`: controller and model server logs.

## Runtime setup

1. Obtain a compatible llamafile release manually from [Mozilla AI llamafile releases](https://github.com/Mozilla-Ocho/llamafile/releases).
2. Select the executable matching your OS and CPU architecture.
3. Place it in the matching `Runtime` subfolder. On Windows the expected name is `llamafile-0.10.6.exe`; on Linux/macOS it is `llamafile-0.10.6`.
4. Do not put it in the controller folder. PortableAI will not fetch or update it.

## Model setup

1. Select a GGUF model from a trusted upstream source. Check its license and usage terms yourself.
2. Place it anywhere under `Models/`, preferably in a descriptive category subfolder such as `Models/reasoning/` or `Models/vision/`.
3. Vision-language models may require a matching multimodal projector (`mmproj*.gguf`). Keep the projector alongside the model and select it in the control panel.
4. Select **Add / Detect Models**. Scanning is local and does not download anything.
5. Choose the model, configure context and compute settings, then start its server.

## Open the model interface

The model server listens on the selected loopback address, normally `http://127.0.0.1:8080`. Open that address in a browser. If prompted for an API key, copy the key from the server card in PortableAI and paste it into the model interface. The key is generated locally for that server instance.

## Offline operation

PortableAI forces the runtime into offline mode and launches only local files. Initial setup requires you to manually supply the runtime and model files. External links in the catalog are references only.

## License terminology

**Open source** means the model is distributed under a license that meets the applicable open-source definition. **Open weights** means downloadable weights are available; it does not necessarily mean the license is an OSI-approved open-source license. A catalog license flag refers to upstream repository metadata and does not establish the provenance of a particular local file.
