import { useAuth } from "../auth/AuthProvider";
import AuthScreen, { AuthHeading } from "./AuthScreen";
import ChangePasswordForm from "./ChangePasswordForm";
import { Button } from "./ui";

/**
 * The only screen of a session whose password was issued by somebody else (the generated admin
 * password, a temporary one from a reset). The server refuses everything else until it is
 * replaced; signing out is the one other way out.
 */
export default function SetNewPassword() {
  const auth = useAuth();
  return (
    <AuthScreen
      footer={
        <Button variant="link" onClick={() => void auth.logout()}>
          Sign out
        </Button>
      }
    >
      <div>
        <AuthHeading>Set a new password</AuthHeading>
        <p className="text-muted-foreground">
          This is your first sign-in. Choose a new password to continue.
        </p>
      </div>
      <ChangePasswordForm forced />
    </AuthScreen>
  );
}
