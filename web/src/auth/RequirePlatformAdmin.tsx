import { Outlet } from "react-router-dom";
import Layout from "../components/Layout";
import NotFound from "../pages/NotFound";
import { useAuth } from "./AuthProvider";

/**
 * Renders the /admin routes only for a platform admin. Everybody else gets the not-found page
 * inside the normal application shell, so the area is not even revealed; the API refuses them
 * with 403 anyway.
 */
export default function RequirePlatformAdmin() {
  const auth = useAuth();
  if (auth.session?.user.isPlatformAdmin !== true) {
    return (
      <Layout>
        <NotFound />
      </Layout>
    );
  }
  return <Outlet />;
}
