import {webview2Bridge} from './webview2-bridge.mjs';
import {applyI18n, setLocale, t, formatShortTime} from './i18n.mjs';

export function notificationText(card) {
  if (card.count) return {
    result:t('common.notification.finished'),
    name:t('common.notification.summary',{count:card.count}),
    detail:t('common.notification.summary_detail',{success:card.count-card.failed,failed:card.failed}),
    action:t('common.notification.commands'),
  };
  return {
    result:t(card.result==='succeeded'?'common.notification.succeeded':card.reason==='timeout'?'common.notification.timeout':'common.notification.failed'),
    name:card.name,
    detail:t('common.notification.detail',{seconds:card.elapsed_seconds.toFixed(1),time:formatShortTime(card.started_at)}),
    action:t('common.command.logs'),
  };
}

export function notificationView({bridge,document,frame=requestAnimationFrame}) {
  const byID=id=>document.getElementById(id), root=document.documentElement;
  const main=byID('notification'), open=byID('notification-open'), close=byID('notification-close');
  let epoch='',card=null,hover=false,focused=false,render=0;
  const post=(kind,fields={})=>bridge.send({version:1,kind,generation:epoch,...fields});
  const held=()=>post('hover',{id:card?.id||0,paused:hover||focused});
  function paint() {
    if (!card) return;
    const text=notificationText(card);
    root.dataset.result=card.result==='failed'||card.failed>0?'failed':'succeeded';
    byID('notification-name').textContent=text.name;
    byID('notification-name').title=text.name;
    byID('notification-result').textContent=text.result;
    byID('notification-detail').textContent=text.detail;
    open.textContent=text.action;open.disabled=Boolean(card.opening);
    main.setAttribute('aria-label',t('common.notification.accessible',{name:text.name,result:text.result,id:card.run_id||card.id}));
    byID('notification-error').hidden=!card.error_key;
    byID('notification-error').textContent=card.error_key?t(card.error_key):'';
  }
  main.addEventListener('pointerenter',()=>{hover=true;held()});
  main.addEventListener('pointerleave',()=>{hover=false;held()});
  main.addEventListener('focusin',()=>{focused=true;held()});
  main.addEventListener('focusout',event=>{focused=main.contains(event.relatedTarget);held()});
  open.addEventListener('click',()=>{if(card&&!card.opening)post('activate',{id:card.id})});
  close.addEventListener('click',()=>{if(card)post('dismiss',{id:card.id})});
  main.addEventListener('keydown',event=>{if(event.key==='Escape'&&card){event.preventDefault();post('dismiss',{id:card.id})}});
  bridge.subscribe(message=>{
    if(message.kind==='init'){
      epoch=message.generation;root.dataset.theme=message.theme;setLocale(message.locale);applyI18n(document);post('initialized');
    } else if(message.kind==='notification'){
      const changed=card?.id!==message.card.id;
      card=message.card;root.dataset.theme=message.theme;setLocale(message.locale);applyI18n(document);paint();
      if(changed){hover=false;focused=false}
      const generation=++render,id=card.id,layout=message.layout;
      frame(()=>frame(()=>{
        if(generation===render)post('rendered',{id,layout,height:Math.ceil(main.getBoundingClientRect().height)});
      }));
    } else if(message.kind==='theme'){root.dataset.theme=message.theme}
    else if(message.kind==='locale'){setLocale(message.locale);applyI18n(document);paint()}
    else if(message.kind==='visibility'&&!message.visible){hover=false;focused=false;held()}
  });
  post('ready');
}

if(globalThis.window?.chrome?.webview) notificationView({bridge:webview2Bridge(),document});
