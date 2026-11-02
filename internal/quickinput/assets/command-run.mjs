import {t} from './i18n.mjs';
import {runKey,modeKey,reasonKey} from '../../../web/command-run.mjs';
export {runBusy,runStoppable} from '../../../web/command-run.mjs';
export const runLabel = run => t(runKey(run));
export const modeLabel = command => t(modeKey(command));
export const reasonLabel = run => t(reasonKey(run));
