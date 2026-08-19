// ai-form-backend console - AGPL-3.0
// 购买套餐:订阅(base)与加油包(pack)分区展示;加油包仅限持有有效订阅的用户购买(后端同样门禁)。
// 点"购买" → 易支付收银台(表单跳转,目前仅支付宝);回跳带 ?pay= 提示结果。
// 权益文案依据插件仓库《积分与订阅计费方案》:月费买插件与自动化能力,积分只花在"建方案 / AI 生成"两件事上。
import React, { useContext, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Check, Lock } from 'lucide-react';
import { get, post } from '../api.js';
import { MeContext } from '../App.jsx';
import { Button, Tag, Notice, Spinner, toast, fmtNum } from '../ui';

// 用隐藏表单 POST 跳转到易支付收银台
function submitPayForm(url, params) {
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = url;
  for (const [k, v] of Object.entries(params || {})) {
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = k;
    input.value = v;
    form.appendChild(input);
  }
  document.body.appendChild(form);
  form.submit();
}

const yuan = (cents) => (cents / 100).toFixed(2).replace(/\.00$/, '');
const perThousand = (p) => (p.priceCents * 10 / p.credits).toFixed(1).replace(/\.0$/, '');

function baseBenefits(p, bonus) {
  return [
    ['插件全部功能,固定流程录入不限量', '录入方案建好后反复使用,越用越稳,不按条数收费'],
    ['判断类小 AI 免费、不限次数', '页面判断、找按钮、表单选型、层级判断、大白话指令,全部 0 积分'],
    ['诊断与自愈免费', '错误诊断、根因分析、一键修复、页面改版重识别——软件没处理好,修复不再收钱'],
    [`每月含 ${fmtNum(p.credits)} 积分`, '只花在两件事上:建立新的录入方案、AI 生成字段内容'],
    bonus && [`首次开通加赠 ${fmtNum(bonus.credits)} 积分`, `${bonus.durationDays} 天有效,不随首月清零,覆盖前期学习消耗`],
    ['积分不够随时加包', '购买积分 365 天有效;订阅断档时冻结不作废,复订即恢复'],
  ].filter(Boolean);
}

export default function TopUp() {
  const { me, reload } = useContext(MeContext);
  const [data, setData] = useState(null);
  const [buying, setBuying] = useState(0);
  const [sp, setSp] = useSearchParams();

  useEffect(() => {
    get('/v1/subscription/plans').then(setData).catch((e) => { toast.error(e.message); setData({ plans: [] }); });
  }, []);

  useEffect(() => {
    const pay = sp.get('pay');
    if (!pay) return;
    if (pay === 'success') { toast.success('支付成功,套餐已到账'); reload(); }
    else if (pay === 'fail') toast.error('支付未完成');
    else toast.info('支付处理中,稍后在个人中心查看');
    sp.delete('pay');
    setSp(sp, { replace: true });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const buy = async (plan) => {
    setBuying(plan.id);
    try {
      const d = await post('/v1/subscription/pay', { planId: plan.id, method: 'alipay' });
      submitPayForm(d.url, d.params);
    } catch (e) {
      toast.error(e.message);
      setBuying(0);
    }
  };

  if (data === null) return <div className="table-loading"><Spinner size="lg" /></div>;
  const basePlans = data.plans.filter((p) => p.planType === 'base');
  const packPlans = data.plans.filter((p) => p.planType === 'pack');
  const hasBase = (me.buckets || []).some((b) => b.planType === 'base');

  return (
    <div className="topup">
      <section className="topup-section">
        <header className="section-head">
          <h2>个人版订阅</h2>
          <p>月费买的是插件与自动化能力;积分只花在「建立新录入方案」和「AI 生成内容」两件事上</p>
        </header>
        {basePlans.length === 0 && <div className="card"><div className="empty"><span>暂无在售订阅</span></div></div>}
        {basePlans.map((p) => (
          <article key={p.id} className="sub-card">
            <div className="sub-main">
              <div className="sub-kicker">
                <span>{p.name}</span>
                {hasBase && <Tag tone="ok" dot>已开通</Tag>}
              </div>
              <div className="sub-price num">
                <small>¥</small>{yuan(p.priceCents)}
                <span className="sub-unit">/ {p.durationDays === 30 ? '月' : `${p.durationDays} 天`}</span>
              </div>
              <p className="sub-desc">
                {p.durationDays} 天有效,月含积分到期清零
                {data.firstPurchaseBonus && ';首次开通加赠 ' + fmtNum(data.firstPurchaseBonus.credits) + ' 积分'}
              </p>
              <Button className="sub-cta" size="lg" loading={buying === p.id} onClick={() => buy(p)}>
                {hasBase ? `续费 ${p.durationDays} 天` : '立即开通'}
              </Button>
              <p className="sub-note">支付宝支付 · 支付成功自动到账</p>
            </div>
            <ul className="sub-benefits">
              {baseBenefits(p, data.firstPurchaseBonus).map(([title, desc]) => (
                <li key={title}>
                  <Check />
                  <div><b>{title}</b><span>{desc}</span></div>
                </li>
              ))}
            </ul>
          </article>
        ))}
        <div className="spend-rules">
          <div><b>0 积分</b><span>判断 · 诊断 · 自愈 · 失败重试 · 固定流程录入</span></div>
          <div><b>按方案计</b><span>建立一个可复用的录入方案;同一方案永不重复收费,建好并保存后才扣</span></div>
          <div><b>按格计</b><span>AI 主动生成字段内容,按成功的格数计;同格重新生成免费</span></div>
        </div>
      </section>

      <section className="topup-section">
        <header className="section-head">
          <h2>积分加油包</h2>
          <p>订阅用户专享 · 购买积分 365 天有效 · 先花月含积分,再花购买积分</p>
        </header>
        {!hasBase && packPlans.length > 0 && (
          <Notice tone="info">加油包仅限持有有效个人版订阅的用户购买。请先开通个人版,再按需加包。</Notice>
        )}
        {packPlans.length === 0 ? (
          <div className="card"><div className="empty"><span>暂无在售加油包</span></div></div>
        ) : (
          <div className="packs">
            {packPlans.map((p) => (
              <article key={p.id} className={'pack' + (hasBase ? '' : ' locked')}>
                <div className="pack-top">
                  <span className="pack-name">{p.name}</span>
                  {!hasBase && <Lock />}
                </div>
                <div className="pack-credits num">{fmtNum(p.credits)}<small>积分</small></div>
                <div className="pack-price num"><small>¥</small>{yuan(p.priceCents)}</div>
                <div className="pack-meta">约 ¥{perThousand(p)} / 千积分 · {p.durationDays} 天有效</div>
                <Button block variant={hasBase ? 'primary' : 'default'} disabled={!hasBase} loading={buying === p.id} onClick={() => buy(p)}>
                  {hasBase ? '购买加油包' : '需先开通订阅'}
                </Button>
              </article>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
