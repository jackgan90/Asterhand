import {t} from './i18n.mjs';
import {runKey,reasonKey,summaryParts} from '../../../web/command-run.mjs';
export {runBusy,runStoppable,canLaunch,runTone} from '../../../web/command-run.mjs';
export const runLabel = run => t(runKey(run));
export const summaryLabel = command => summaryParts(command,t).join(' · ');
export const reasonLabel = run => t(reasonKey(run));
