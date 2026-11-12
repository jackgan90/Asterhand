import {liveEditors} from '../../../web/live-editors.mjs';

export function connectLiveInput(bridge, editor, chat) {
  const live = liveEditors();
  const slot = live.register({
    target: () => ({
      kind: 'chat', node_id: live.nodeID, role_id: 'default',
      conversation_id: chat.liveTarget().conversation_id
    }),
    targetVersion: () => chat.liveTarget().selection,
    busy: () => !editor.liveState(),
    version: () => editor.liveState()?.revision,
    read: () => ({text: editor.liveState().text, mode: 'message'}),
    apply(value) {
      if (value.mode && value.mode !== 'message') {
        throw Object.assign(new Error('invalid_parameters'), {code:'invalid_parameters'});
      }
      editor.replaceLive(value.text);
    },
    commit: (id, snapshot) => chat.submitLive(id, snapshot.target.conversation_id)
  });
  const unsubscribe = bridge.subscribe(message => {
    if (message.kind === 'init') {
      slot.invalidate();
      live.attach();
    }
    if (message.kind === 'editor_request') {
      void live.receive(message, ({reply}) => editor.post('editor_reply', {editor_reply:reply}));
    }
  });
  window.addEventListener('pagehide', () => {
    live.stop();
    unsubscribe();
  }, {once:true});
}
