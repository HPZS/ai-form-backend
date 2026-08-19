// ai-form-backend console - AGPL-3.0
// 控制台外壳:左侧栏(品牌 / 导航 / 积分 / 账号) + 内容区;窄屏折叠为顶栏 + 抽屉。
import React, { useContext, useEffect, useState } from 'react';
import { Outlet, NavLink, useNavigate, useLocation } from 'react-router-dom';
import {
  LayoutDashboard, CreditCard, ReceiptText, Tags, SlidersHorizontal, Server, Users,
  LogOut, ChevronsUpDown, Menu as MenuIcon, X, ArrowRight,
} from 'lucide-react';
import { MeContext } from '../App.jsx';
import { logout } from '../api.js';
import { Button, Menu, fmtNum } from '../ui';

const USER_NAV = [
  { to: '/', label: '个人中心', icon: <LayoutDashboard /> },
  { to: '/topup', label: '购买套餐', icon: <CreditCard /> },
  { to: '/ledger', label: '积分流水', icon: <ReceiptText /> },
];
const ADMIN_NAV = [
  { to: '/admin/plans', label: '套餐管理', icon: <Tags /> },
  { to: '/admin/capabilities', label: '能力配置', icon: <SlidersHorizontal /> },
  { to: '/admin/upstreams', label: 'AI 上游', icon: <Server /> },
  { to: '/admin/users', label: '用户管理', icon: <Users /> },
];
const PAGE_META = {
  '/': ['个人中心', '账户额度、有效订阅与登录设置'],
  '/topup': ['购买套餐', '订阅个人版解锁全部功能,积分不够随时加包'],
  '/ledger': ['积分流水', '每一笔充值与消费,只增不改'],
  '/admin/plans': ['套餐管理', '套餐价格、额度与销售状态;改价只影响新订单'],
  '/admin/capabilities': ['能力配置', '默认模型统一设置,单个能力按需覆盖计费与模型'],
  '/admin/upstreams': ['AI 上游', 'OpenAI 兼容端点,按优先级故障切换'],
  '/admin/users': ['用户管理', '查看用户、管理额度与账号状态'],
};

export function Brand() {
  return (
    <div className="brand">
      <span className="brand-mark" aria-hidden="true">AI</span>
      <span>智能录入助手</span>
    </div>
  );
}

export default function ConsoleLayout() {
  const { me } = useContext(MeContext);
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const [open, setOpen] = useState(false);
  const [title, desc] = PAGE_META[pathname] || ['控制台', ''];

  useEffect(() => { setOpen(false); }, [pathname]);
  useEffect(() => { document.title = `${title} · AI 智能录入助手`; }, [title]);

  const navItem = (it) => (
    <NavLink key={it.to} to={it.to} end={it.to === '/'} className={({ isActive }) => 'nav-item' + (isActive ? ' active' : '')}>
      {it.icon}{it.label}
    </NavLink>
  );

  return (
    <div className="shell">
      <header className="topbar">
        <Brand />
        <Button variant="ghost" icon={open ? <X /> : <MenuIcon />} aria-label="菜单" onClick={() => setOpen((o) => !o)} />
      </header>
      <div className={'scrim' + (open ? ' open' : '')} onClick={() => setOpen(false)} />

      <aside className={'sidebar' + (open ? ' open' : '')}>
        <Brand />
        <nav className="nav">
          {USER_NAV.map(navItem)}
          {me.role === 'admin' && (
            <div className="nav-group">
              <div className="nav-group-title">系统管理</div>
              {ADMIN_NAV.map(navItem)}
            </div>
          )}
        </nav>
        <div className="sidebar-foot">
          <NavLink to="/topup" className="credit-chip" title="购买套餐">
            <div>
              <div className="credit-chip-label">可用积分</div>
              <div className="credit-chip-value num">{fmtNum(me.available)}</div>
            </div>
            <ArrowRight />
          </NavLink>
          <Menu align="start" side="top" items={[
            { header: me.email },
            { label: '退出登录', icon: <LogOut />, danger: true, onSelect: logout },
          ]}>
            <button type="button" className="user-button">
              <span className="avatar">{me.email[0]}</span>
              <span className="user-meta">
                <span className="user-email">{me.email}</span>
                <span className="user-role">{me.role === 'admin' ? '管理员' : '个人账户'}</span>
              </span>
              <ChevronsUpDown />
            </button>
          </Menu>
        </div>
      </aside>

      <div className="main">
        <main className="content">
          <div className="page-head" key={pathname}>
            <div>
              <h1 className="page-title">{title}</h1>
              {desc && <p className="page-desc">{desc}</p>}
            </div>
          </div>
          <div className="page-body" key={pathname + '#body'}><Outlet /></div>
        </main>
      </div>
    </div>
  );
}
