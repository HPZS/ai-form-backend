// 能力说明与价格来自计费服务；打开期间同步配置，不写死收费清单。
import React, { useEffect, useState } from 'react';
import { api } from '../api.js';
import { Dialog, Table, Notice } from '../ui';
import './CapabilityPricing.css';

export default function CapabilityPricing({open,onClose}) {
  const [data,setData]=useState(null);
  const [error,setError]=useState('');
  useEffect(()=>{
    if(!open)return;
    let active=true,timer,controller;
    setData(null);setError('');
    async function refresh(){
      if(document.hidden){timer=setTimeout(refresh,2000);return;}
      controller=new AbortController();
      try {
        const next=await api('/v1/billing/catalog',{signal:controller.signal,cache:'no-store'});
        if(!Array.isArray(next.capabilities)||!next.capabilities.length)throw new Error('能力目录为空或格式异常');
        if(active){setData(next.capabilities);setError('');}
      } catch(e) { if(active&&e.name!=='AbortError')setError('最新价格暂时无法读取：'+e.message); }
      finally {if(active)timer=setTimeout(refresh,2000);}
    }
    refresh();
    return()=>{active=false;clearTimeout(timer);controller?.abort();};
  },[open]);
  const columns=[
    {title:'AI 能力',key:'name',render:r=><div><div className="cell-title">{r.name}</div><div className="cell-sub">{r.description}</div></div>},
    {title:'计费方式',key:'mode',width:140,render:r=>r.valid?(r.mode==='included'?'订阅包含':'按次扣积分'):'配置待修复'},
    {title:'积分 / 次',key:'credits',width:110,render:r=>r.valid?(r.mode==='included'?'0 积分':`${r.credits} 积分`):'—'},
  ];
  return <Dialog open={open} onClose={onClose} title="AI 能力与积分价格" width={900}>
    <p className="muted">订阅包含的能力不额外扣积分；按次能力按每次成功调用结算。本地复用不调用 AI，同一请求重传不重复扣费。表格每 2 秒同步最新配置。</p>
    {error&&<Notice tone="warn">{error}{data?'。下表是上次读取结果，请以恢复连接后的最新价格为准。':''}</Notice>}
    <div className="capability-pricing"><Table columns={columns} rows={data||[]} rowKey="capability" loading={data===null&&!error} /></div>
  </Dialog>;
}
