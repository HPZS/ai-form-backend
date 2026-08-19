// ai-form-backend console - AGPL-3.0
import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App.jsx';
import './styles/tokens.css';
import './styles/base.css';
import './styles/ui.css';
import './styles/pages.css';
import './bridge.js'; // 浏览器插件登录态桥接(网站登录 → 插件自动登录)

ReactDOM.createRoot(document.getElementById('root')).render(<App />);
