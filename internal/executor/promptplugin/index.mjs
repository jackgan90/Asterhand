import {readFileSync, statSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {isAbsolute} from 'node:path';

const validID = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value);

function readPlan(planURL) {
  if (statSync(planURL).size > 64 * 1024) return null;
  const plan = JSON.parse(readFileSync(planURL, 'utf8'));
  if (plan.version !== 1 || !validID(plan.instance) || typeof plan.text !== 'string' || plan.text.length > 16384 ||
      !['replace', 'append'].includes(plan.application?.mode) ||
      !/^default-chat-v[12]-[a-f0-9]{16}$/.test(plan.application?.revision) ||
      createHash('sha256').update(plan.text).digest('hex') !== plan.digest) return null;
  return plan;
}

export default {
  id: 'asterhand-chat-policy',
  name: 'Asterhand chat policy',
  register(api) {
    if (typeof api.on !== 'function') return;
    // OpenClaw captures plugin source into a separate immutable directory.
    // The mutable policy path therefore comes only from trusted host config.
    const {policyPath: planURL} = api.pluginConfig ?? {};
    if (typeof planURL !== 'string' || !isAbsolute(planURL)) return;
    const observations = new Map();
    const remember = (run, record) => {
      if (!observations.has(run) && observations.size >= 16) observations.delete(observations.keys().next().value);
      observations.set(run, record);
    };
    // Private metadata only, behind Gateway authentication and operator.read.
    // No transcript, tool, policy mutation, or public UI method is exposed.
    api.registerGatewayMethod?.('asterhand.chat.promptStatus', ({params, respond}) => {
      const record = validID(params.run) ? observations.get(params.run) : null;
      respond(true, {record: record && record.instance === params.instance && record.session === params.session ? record : null});
    }, {scope: 'operator.read', profileAccess: 'independent'});
    let initialPlan = null;
    try { initialPlan = readPlan(planURL); } catch {}
    api.on('before_prompt_build', (_event, context) => {
      try {
        const plan = readPlan(planURL);
        if (!plan || !context.sessionKey?.startsWith('agent:main:asterhand:')) return;
        context.hookInvocation?.assertActive();
        return plan.application.mode === 'replace' ? {systemPrompt: plan.text, toolsAllow: []} :
          {appendSystemContext: plan.text, toolsAllow: []};
      } catch { /* Optional prompt customization never blocks a turn. */ }
    });
    api.on('llm_input', (event, context) => {
      if (!validID(event.runId) || !context.sessionKey?.startsWith('agent:main:asterhand:')) return;
      // Clear the previous attempt before reading/validating anything fallible.
      remember(event.runId, null);
      try {
        let plan = initialPlan;
        try { plan = readPlan(planURL) ?? initialPlan; } catch {}
        // Keep enough expected metadata to revoke an earlier success if a later
        // attempt falls back after the policy file becomes unreadable.
        if (!plan) return;
        const effective = event.systemPrompt ?? '';
        // The embedded harness appends required model-identity context after a replacement.
        const applied = plan.application.mode === 'replace' ? effective.startsWith(plan.text) : effective.includes(plan.text);
        const safeTools = Array.isArray(event.tools) && event.tools.length === 0;
        // Keep only correlation and status, never prompt, body, or credentials.
        const application = {...plan.application, state: applied && safeTools ? 'applied' : 'degraded',
          ...(applied && safeTools ? {} : {reason: 'unconfirmed'})};
        remember(event.runId, {instance: plan.instance, run: event.runId, session: event.sessionId, application});
      } catch { /* Missing observations remain unconfirmed; chat continues. */ }
    });
  },
};
