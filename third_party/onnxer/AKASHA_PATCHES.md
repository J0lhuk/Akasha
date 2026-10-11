# Local compatibility patch

Upstream: github.com/benedoc-inc/onnxer v0.7.0 (MIT). Original package code and license retained.

`runtime.go` delegates shared-library opening to OS-specific files. Windows uses LoadLibraryEx with an explicit path and restricted dependency search, because upstream called Unix-only purego.Dlopen and did not compile on Windows. Unix retains the original Dlopen flags. Akasha uses NewSessionFromReader to avoid upstream's UTF-8 filename path in an ORT API requiring UTF-16 on Windows. No model downloads occur when opening the runtime.

This fork is limited to the runtime packages consumed by Akasha. The application pins model/release archive hashes and tests actual Windows inference. It is not an upstream security audit.

Go 1.26 rejects upstream AddCleanup callbacks capturing the object being cleaned. Callbacks now capture only native handles/release functions; explicit Close stops the cleanup to prevent double release. Tensor inputs retain their Go backing slices and Run keeps values alive across the native call. Native resources are still explicitly closed by Akasha.
