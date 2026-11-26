The Windows build scripts copy `rsm-server.exe` and the `downloads` folder
(client and viewer files) here before building the server setup, which embeds
them. The copies are build output and are ignored by git; this note keeps the
folder (and `go:embed`) valid in a clean checkout.
