export function browserActions(browser = globalThis) {
  return {
    confirm:message => browser.confirm(message),
    prompt:message => browser.prompt(message),
    copy:text => browser.navigator.clipboard.writeText(text),
  };
}
