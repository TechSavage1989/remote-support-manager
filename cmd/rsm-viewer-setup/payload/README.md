The Windows build scripts copy `RemoteSupportViewer.exe` here before building
the installer, which embeds it. The copied file is build output and is ignored
by git; this note keeps the folder (and `go:embed`) valid in a clean checkout.
