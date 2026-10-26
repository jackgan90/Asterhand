export function apiClient({fetch: request = globalThis.fetch, EventSource: Source = globalThis.EventSource} = {}) {
  let csrf = "", authentication = Promise.resolve();
  async function requestJSON(path, options = {}) {
    // The UI only addresses this node; no endpoint or authentication registry.
    if (!path.startsWith("/api/") || /[\\#]/.test(path)) throw new Error("请求必须使用本机同源 API");
    const response = await request(path, {
      ...options,
      headers:{"Content-Type":"application/json", "X-Asterhand-CSRF":csrf, ...options.headers},
    });
    const data = await response.json();
    if (!response.ok) {
      const error = new Error(data.error || `请求失败 (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return data;
  }
  async function api(path, options) {
    // Wait for the complete Cookie/body transaction before issuing UI requests.
    await authentication;
    return requestJSON(path, options);
  }
  const post = (path, data = {}) => api(path, {method:"POST", body:JSON.stringify(data)});
  function authenticate(token, isCurrent = () => true) {
    const transaction = authentication.then(async () => {
      if (!isCurrent()) return;
      const result = await requestJSON(token ? "/api/login" : "/api/session",
        token ? {method:"POST", body:JSON.stringify({token})} : undefined);
      // A sent login can update the Cookie even after its UI session ended.
      // Complete that pair before the next authentication changes either half.
      csrf = result.csrf;
    });
    authentication = transaction.catch(() => {});
    return transaction;
  }
  return {api, post, authenticate, stream:() => new Source("/api/stream")};
}
