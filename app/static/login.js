const form = document.querySelector('#login-form');
const input = document.querySelector('#password');
const error = document.querySelector('#login-error');
const button = document.querySelector('#login-submit');

form.onsubmit = async event => {
  event.preventDefault();
  button.disabled = true;
  error.hidden = true;
  try {
    const response = await fetch('/api/login', {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({password: input.value}),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || 'Sign in failed. Please try again.');
    location.reload();  // the same address now serves the manager
  } catch (problem) {
    error.textContent = problem.message;
    error.hidden = false;
    input.select();
  } finally {
    button.disabled = false;
  }
};
