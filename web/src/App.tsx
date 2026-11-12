import { Route, Routes } from "react-router-dom";
import RequireAuth from "./auth/RequireAuth";
import RequirePermission from "./auth/RequirePermission";
import RequirePlatformAdmin from "./auth/RequirePlatformAdmin";
import AdminLayout from "./components/AdminLayout";
import Layout from "./components/Layout";
import Channels from "./pages/Channels";
import ForgotPassword from "./pages/ForgotPassword";
import HelpRoles from "./pages/HelpRoles";
import Invite from "./pages/Invite";
import Login from "./pages/Login";
import Members from "./pages/Members";
import MonitorDetails from "./pages/MonitorDetails";
import MonitorForm from "./pages/MonitorForm";
import Monitors from "./pages/Monitors";
import NotFound from "./pages/NotFound";
import Profile from "./pages/Profile";
import Projects from "./pages/Projects";
import Register from "./pages/Register";
import ResetPassword from "./pages/ResetPassword";
import Settings from "./pages/Settings";
import StatusPageSettings from "./pages/StatusPage";
import VerifyEmail from "./pages/VerifyEmail";
import AdminEmail from "./pages/admin/AdminEmail";
import AdminRegistration from "./pages/admin/AdminRegistration";
import AdminUserDetails from "./pages/admin/AdminUserDetails";
import AdminUsers from "./pages/admin/AdminUsers";
import AdminOverview from "./pages/admin/AdminOverview";
import AdminOrganizationDetails from "./pages/admin/AdminOrganizationDetails";
import AdminOrganizations from "./pages/admin/AdminOrganizations";

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />
      <Route path="/invite" element={<Invite />} />
      <Route path="/forgot-password" element={<ForgotPassword />} />
      <Route path="/reset-password" element={<ResetPassword />} />
      <Route path="/verify-email" element={<VerifyEmail />} />
      <Route element={<RequireAuth />}>
        <Route element={<Layout />}>
          <Route path="/" element={<Monitors />} />
          <Route path="/monitors/new" element={<MonitorForm mode="create" />} />
          <Route path="/monitors/:id" element={<MonitorDetails />} />
          <Route path="/monitors/:id/edit" element={<MonitorForm mode="edit" />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="/profile" element={<Profile />} />
          <Route path="/members" element={<Members />} />
          <Route path="/help/roles" element={<HelpRoles />} />
          <Route element={<RequirePermission permission="project.read" />}>
            <Route path="/projects" element={<Projects />} />
          </Route>
          <Route element={<RequirePermission permission="channel.read" />}>
            <Route path="/channels" element={<Channels />} />
          </Route>
          <Route element={<RequirePermission permission="statuspage.read" />}>
            <Route path="/status-page" element={<StatusPageSettings />} />
          </Route>
          <Route path="*" element={<NotFound />} />
        </Route>
        <Route path="/admin" element={<RequirePlatformAdmin />}>
          <Route element={<AdminLayout />}>
            <Route index element={<AdminOverview />} />
            <Route path="organizations" element={<AdminOrganizations />} />
            <Route path="organizations/:id" element={<AdminOrganizationDetails />} />
            <Route path="users" element={<AdminUsers />} />
            <Route path="users/:id" element={<AdminUserDetails />} />
            <Route path="email" element={<AdminEmail />} />
            <Route path="registration" element={<AdminRegistration />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Route>
      </Route>
    </Routes>
  );
}
