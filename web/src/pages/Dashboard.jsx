// ai-form-backend console - AGPL-3.0
// 个人中心:可用积分(主视觉)、账号信息、额度桶进度、密码设置。
import React, { useContext, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ArrowRight, Hourglass } from 'lucide-react';
import { MeContext } from '../App.jsx';
import { post } from '../api.js';
import { Card, Button, Input, Tag, Progress, toast, fmtNum, fmtDate } from '../ui';

const BUCKET = {
  base: { label: '订阅', tone: 'accent' },
  trial: { label: '试用', tone: 'neutral' },
  pack: { label: '加油包', tone: 'ok' },
  bonus: { label: '赠送', tone: 'warn' },
};

function daysLeft(endsAt) {
  const d = Math.ceil((new Date(endsAt) - Date.now()) / 86400000);
  return d <= 0 ? '今天到期' : `${d} 天后到期`;
}

export default function Dashboard() {
  const { me } = useContext(MeContext);
  const navigate = useNavigate();
  const [pw, setPw] = useState('');
  const [saving, setSaving] = useState(false);

  const setPassword = async () => {
    setSaving(true);
    try {
      await post('/v1/auth/set-password', { password: pw });
      setPw('');
      toast.success('密码已设置,下次可直接密码登录');
    } catch (e) { toast.error(e.message); }
    finally { setSaving(false); }
  };

  const buckets = me.buckets || [];

  return (
    <div className="grid dash">
      <Card className="hero span-2">
        <div className="hero-label">可用积分</div>
        <div className="hero-value num">{fmtNum(me.available)}</div>
        <p className="hero-sub">
          {buckets.length > 0
            ? `来自 ${buckets.length} 个有效额度桶 · 最近一个 ${daysLeft(buckets[0].endsAt)}`
            : '暂无有效额度,开通订阅后即可在插件中使用 AI 能力'}
        </p>
        <div className="hero-actions">
          <Button variant="primary" onClick={() => navigate('/topup')}>购买套餐<ArrowRight /></Button>
          <Button variant="ghost" onClick={() => navigate('/ledger')}>查看流水</Button>
        </div>
      </Card>

      <Card title="账号">
        <dl className="kv">
          <dt>邮箱</dt><dd className="ellipsis" title={me.email}>{me.email}</dd>
          <dt>角色</dt><dd>{me.role === 'admin' ? <Tag tone="accent">管理员</Tag> : <Tag>个人账户</Tag>}</dd>
          <dt>额度桶</dt><dd className="num">{buckets.length} 个生效中</dd>
        </dl>
      </Card>

      <Card className="span-2" title="我的订阅" extra="到期后剩余积分清零,历史保留">
        {buckets.length === 0 ? (
          <div className="empty"><Hourglass /><span>暂无有效订阅</span>
            <Button size="sm" variant="ghost" onClick={() => navigate('/topup')}>去购买套餐 →</Button>
          </div>
        ) : (
          <ul className="bucket-list">
            {buckets.map((b, i) => {
              const meta = BUCKET[b.planType] || { label: b.planType || '额度', tone: 'neutral' };
              return (
                <li key={i} className="bucket">
                  <div className="bucket-head">
                    <Tag tone={meta.tone}>{meta.label}</Tag>
                    <span className="bucket-remain num"><b>{fmtNum(b.remaining)}</b> / {fmtNum(b.total)}</span>
                  </div>
                  <Progress value={b.total ? b.remaining / b.total : 0} />
                  <div className="bucket-foot">
                    <span>{daysLeft(b.endsAt)}</span>
                    <span className="num">{fmtDate(b.endsAt)}</span>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </Card>

      <Card title="登录密码">
        <p className="muted" style={{ marginBottom: 14 }}>
          设置后可用「邮箱 + 密码」直接登录;忘记密码时用验证码登录再回这里重设。
        </p>
        <div className="stack-8">
          <Input type="password" autoComplete="new-password" value={pw} onChange={setPw} placeholder="新密码(至少 8 位)" />
          <Button loading={saving} disabled={pw.length < 8} onClick={setPassword}>保存密码</Button>
        </div>
      </Card>
    </div>
  );
}
