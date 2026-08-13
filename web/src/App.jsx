// ai-form-backend console - AGPL-3.0
import React, { useEffect, useState } from 'react';
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { Spin } from '@douyinfe/semi-ui';
import { get, hasTokens } from './api.js';
import Login from './pages/Login.jsx';
import Sso from './pages/Sso.jsx';
import ConsoleLayout from './components/Layout.jsx';
import Dashboard from './pages/Dashboard.jsx';
import TopUp from './pages/TopUp.jsx';
import Ledger from './pages/Ledger.jsx';
import Plans from './pages/admin/Plans.jsx';
import Capabilities from './pages/admin/Capabilities.jsx';
import Upstreams from './pages/admin/Upstreams.jsx';
import Users from './pages/admin/Users.jsx';

export const MeContext = React.createContext(null);

function Protected({ children }) {
  const [me, setMe] = useState(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    if (!hasTokens()) { setLoading(false); return; }
    get('/v1/me').then(setMe).catch(() => {}).finally(() => setLoading(false));
  }, []);
  if (loading) return <div className="app-loading"><Spin size="large" tip="正在加载控制台..." /></div>;
  if (!me) return <Navigate to="/login" replace />;
  return (
    <MeContext.Provider value={{ me, reload: () => get('/v1/me').then(setMe) }}>
      {children}
    </MeContext.Provider>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="/sso" element={<Sso />} />
        <Route path="/" element={<Protected><ConsoleLayout /></Protected>}>
          <Route index element={<Dashboard />} />
          <Route path="topup" element={<TopUp />} />
          <Route path="ledger" element={<Ledger />} />
          <Route path="admin/plans" element={<Plans />} />
          <Route path="admin/capabilities" element={<Capabilities />} />
          <Route path="admin/upstreams" element={<Upstreams />} />
          <Route path="admin/users" element={<Users />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}
