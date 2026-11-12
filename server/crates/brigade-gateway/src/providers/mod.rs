//! The services a gateway calls (`supabase/functions/_shared/providers/`).
//!
//! [`resend`] and [`postmark`] are the mail providers a shipped connector wraps. Each is the
//! [`Provider`](crate::connector::Provider) of one service: what it reads of the service's webhook and
//! answers, and the requests it has the caller make. Everything that is one provider's own lives in its
//! module; the connector and the core see only the shapes of [`crate::mail`] and [`crate::connector`].
//!
//! [`slack`] is the Slack Web API, as much of it as the Slack gateway uses: the request of each call and
//! the reading of its answer.

pub mod postmark;
pub mod resend;
pub mod slack;

pub use postmark::Postmark;
pub use resend::Resend;
pub use slack::Slack;
