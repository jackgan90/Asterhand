// This adapter owns the only direct WebView2 API dependency. Protocol and draft
// state remain in the editor; native source/version/generation checks remain Go.
export function webview2Bridge(host = window.chrome.webview) {
  return {
    send:message => host.postMessage(message),
    subscribe(callback) {
      const listener = event => callback(event.data);
      host.addEventListener("message", listener);
      return () => host.removeEventListener("message", listener);
    },
  };
}
