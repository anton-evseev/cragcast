# Cragcast

Read the following documents to understand the project:
- `docs/product.md` -- product behavior and requirements from a user viewpoint
- `docs/architecture.md` -- description of the project architecture
- `docs/weather-scoring.md` -- description of the weather scoring algorithm
- `docs/coding-guidelines.md` -- Go conventions


## Token budget
- Keep tool output small: pipe through `head`/`tail`/`grep`, use `rg -l` before reading, read files by line range, never `cat` large or generated files (data/, gen/, bundle/, bndl/, binaries).
- Don't view screenshots or images unless asked. Verify UI with curl, DOM queries or tests instead. If an image is needed, take one, at one viewport.
- Don't use the browser/computer-use tools unless asked.
- Run tests narrowly (`go test ./pkg/...`, `-run TestX`) and show only failures.
- Don't re-read files you've already read or just edited.
- Keep replies short; no recap of what was done.
- When a task is finished, suggest starting a new thread for the next one.
